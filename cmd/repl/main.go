package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/attic-labs/noms/go/diff"
	"github.com/attic-labs/noms/go/spec"
	"github.com/attic-labs/noms/go/types"
	jn "github.com/attic-labs/noms/go/util/json"
	"github.com/attic-labs/noms/go/util/outputpager"
	"github.com/mgutz/ansi"
	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"roci.dev/diff-server/util/chk"
	"roci.dev/diff-server/util/kp"
	rlog "roci.dev/diff-server/util/log"
	"roci.dev/diff-server/util/tbl"
	"roci.dev/diff-server/util/version"
	"roci.dev/replicache-client/db"
	execpkg "roci.dev/replicache-client/exec"
)

const (
	dropWarning = "This command deletes an entire database and its history. This operations is not recoverable. Proceed? y/n\n"
)

type opt struct {
	Args     []string
	OutField string
}

func main() {
	impl(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Exit)
}

func impl(args []string, in io.Reader, out, errs io.Writer, exit func(int)) {
	app := kingpin.New("replicant", "Conflict-Free Replicated Database")
	app.ErrorWriter(errs)
	app.UsageWriter(errs)
	app.Terminate(exit)

	v := app.Flag("version", "Prints the version of Replicant - same as the 'version' command.").Short('v').Bool()
	auth := app.Flag("auth", "The authorization token to pass to db when connecting.").String()
	sps := app.Flag("db", "The database to connect to. Both local and remote databases are supported. For local databases, specify a directory path to store the database in. For remote databases, specify the http(s) URL to the database (usually https://serve.replicate.to/<mydb>).").PlaceHolder("/path/to/db").Required().String()
	tf := app.Flag("trace", "Name of a file to write a trace to").OpenFile(os.O_RDWR|os.O_CREATE, 0644)
	cpu := app.Flag("cpu", "Name of file to write CPU profile to").OpenFile(os.O_RDWR|os.O_CREATE, 0644)

	var sp *spec.Spec
	getSpec := func() (spec.Spec, error) {
		if sp != nil {
			return *sp, nil
		}
		s, err := spec.ForDatabase(*sps)
		if err != nil {
			return spec.Spec{}, err
		}
		s.Options.Authorization = *auth
		return s, nil
	}

	var rdb *db.DB
	getDB := func() (db.DB, error) {
		if rdb != nil {
			return *rdb, nil
		}
		sp, err := getSpec()
		if err != nil {
			return db.DB{}, err
		}
		r, err := db.Load(sp)
		if err != nil {
			return db.DB{}, err
		}
		rdb = r
		return *r, nil
	}
	app.PreAction(func(pc *kingpin.ParseContext) error {
		if *v {
			fmt.Println(version.Version())
			exit(0)
		}
		return nil
	})

	stopCPUProfile := func() {
		if *cpu != nil {
			pprof.StopCPUProfile()
		}
	}
	stopTrace := func() {
		if *tf != nil {
			trace.Stop()
		}
	}
	defer stopTrace()
	defer stopCPUProfile()

	app.Action(func(pc *kingpin.ParseContext) error {
		if pc.SelectedCommand == nil {
			return nil
		}

		// Init logging
		logOptions := rlog.Options{}
		rlog.Init(errs, logOptions)

		if *tf != nil {
			err := trace.Start(*tf)
			if err != nil {
				return err
			}
		}
		if *cpu != nil {
			err := pprof.StartCPUProfile(*tf)
			if err != nil {
				return err
			}
		}
		c := make(chan os.Signal)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			stopTrace()
			stopCPUProfile()
			os.Exit(1)
		}()

		return nil
	})

	has(app, getDB, out)
	get(app, getDB, out)
	scan(app, getDB, out, errs)
	put(app, getDB, in)
	del(app, getDB, out)
	exec(app, getDB, out)
	sync(app, getDB)
	drop(app, getSpec, in, out)
	logCmd(app, getDB, out)

	if len(args) == 0 {
		app.Usage(args)
		return
	}

	_, err := app.Parse(args)
	if err != nil {
		fmt.Fprintln(errs, err.Error())
		exit(1)
	}
}

type gdb func() (db.DB, error)
type gsp func() (spec.Spec, error)

func exec(parent *kingpin.Application, gdb gdb, out io.Writer) {
	kc := parent.Command("exec", "Execute a function from the bundle in stdin.")

	bundle := kc.Flag("bundle", "Filename of bundle to execute").Required().File()
	name := kc.Arg("name", "Name of function from bundle to execute.").Required().String()
	raw := kc.Arg("args", "JSON-formatted arguments to the function. For convenience, bare strings are also supported.").Strings()

	parse := func(s string, noms types.ValueReadWriter) (types.Value, error) {
		switch s {
		case "true":
			return types.Bool(true), nil
		case "false":
			return types.Bool(false), nil
		}
		switch s[0] {
		case '[', '{', '"':
			return jn.FromJSON(strings.NewReader(s), noms, jn.FromOptions{})
		default:
			if f, err := strconv.ParseFloat(s, 10); err == nil {
				return types.Number(f), nil
			}
		}
		return types.String(s), nil
	}

	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}

		buf := &bytes.Buffer{}
		_, err = io.Copy(buf, *bundle)
		chk.NoError(err)
		err = db.PutBundle(buf.Bytes())

		args := make([]types.Value, 0, len(*raw))
		for _, r := range *raw {
			v, err := parse(r, db.Noms())
			if err != nil {
				return err
			}
			args = append(args, v)
		}
		output, err := db.Exec(*name, types.NewList(db.Noms(), args...))
		if output != nil {
			types.WriteEncodedValue(out, output)
		}
		return err
	})
}

func has(parent *kingpin.Application, gdb gdb, out io.Writer) {
	kc := parent.Command("has", "Check whether a value exists in the database.")
	id := kc.Arg("id", "id of the value to check for").Required().String()
	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}
		ok, err := db.Has(*id)
		if err != nil {
			return err
		}
		if ok {
			out.Write([]byte("true\n"))
		} else {
			out.Write([]byte("false\n"))
		}
		return nil
	})
}

func get(parent *kingpin.Application, gdb gdb, out io.Writer) {
	kc := parent.Command("get", "Reads a value from the database.")
	id := kc.Arg("id", "id of the value to get").Required().String()
	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}
		v, err := db.Get(*id)
		if err != nil {
			return err
		}
		if v == nil {
			return nil
		}
		return jn.ToJSON(v, out, jn.ToOptions{Lists: true, Maps: true, Indent: "  "})
	})
}

func scan(parent *kingpin.Application, gdb gdb, out, errs io.Writer) {
	kc := parent.Command("scan", "Scans values in-order from the database.")
	opts := execpkg.ScanOptions{
		Start: &execpkg.ScanBound{
			ID:    &execpkg.ScanID{},
			Index: new(uint64),
		},
	}
	kc.Flag("prefix", "prefix of values to return").StringVar(&opts.Prefix)
	kc.Flag("start-id", "id of the value to start scanning at").StringVar(&opts.Start.ID.Value)
	kc.Flag("start-id-exclusive", "id of the value to start scanning at").BoolVar(&opts.Start.ID.Exclusive)
	kc.Flag("start-index", "id of the value to start scanning at").Uint64Var(opts.Start.Index)
	kc.Flag("limit", "maximum number of items to return").IntVar(&opts.Limit)
	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}
		items, err := db.Scan(opts)
		if err != nil {
			fmt.Fprintln(errs, err)
			return nil
		}
		for _, it := range items {
			fmt.Fprintf(out, "%s: %s\n", it.ID, types.EncodedValue(it.Value.Value))
		}
		return nil
	})
}

func put(parent *kingpin.Application, gdb gdb, in io.Reader) {
	kc := parent.Command("put", "Reads a JSON-formated value from stdin and puts it into the database.")
	id := kc.Arg("id", "id of the value to put").Required().String()
	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}
		v, err := jn.FromJSON(in, db.Noms(), jn.FromOptions{})
		if err != nil {
			return err
		}
		return db.Put(*id, v)
	})
}

func del(parent *kingpin.Application, gdb gdb, out io.Writer) {
	kc := parent.Command("del", "Deletes an item from the database.")
	id := kc.Arg("id", "id of the value to delete").Required().String()
	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}
		ok, err := db.Del(*id)
		if err != nil {
			return err
		}
		if !ok {
			out.Write([]byte("No such id.\n"))
		}
		return nil
	})
}

func sync(parent *kingpin.Application, gdb gdb) {
	kc := parent.Command("sync", "Sync with a replicant server.")
	bundle := kc.Flag("bundle", "Source of bundle functions").Required().File()
	remoteSpec := kp.DatabaseSpec(kc.Arg("remote", "Server to sync with. See https://github.com/attic-labs/noms/blob/master/doc/spelling.md#spelling-databases.").Required())

	kc.Action(func(_ *kingpin.ParseContext) error {
		db, err := gdb()
		if err != nil {
			return err
		}

		buf := &bytes.Buffer{}
		_, err = io.Copy(buf, *bundle)
		chk.NoError(err)
		err = db.PutBundle(buf.Bytes())

		// TODO: progress
		return db.RequestSync(*remoteSpec, nil)
	})
}

func drop(parent *kingpin.Application, gsp gsp, in io.Reader, out io.Writer) {
	kc := parent.Command("drop", "Deletes a replicant database and its history.")

	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	kc.Action(func(_ *kingpin.ParseContext) error {
		w.WriteString(dropWarning)
		w.Flush()
		answer, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		answer = strings.TrimSpace(answer)
		if answer != "y" {
			return nil
		}
		sp, err := gsp()
		if err != nil {
			return err
		}
		noms := sp.GetDatabase()
		_, err = noms.Delete(noms.GetDataset(db.LOCAL_DATASET))
		return err
	})
}

func logCmd(parent *kingpin.Application, gdb gdb, out io.Writer) {
	kc := parent.Command("log", "Displays the history of a replicant database.")
	np := kc.Flag("no-pager", "supress paging functionality").Bool()

	kc.Action(func(_ *kingpin.ParseContext) error {
		d, err := gdb()
		if err != nil {
			return err
		}
		c := d.Head()
		r, err := d.RemoteHead()
		if err != nil {
			return err
		}
		inRemote := false

		if !*np {
			pgr := outputpager.Start()
			defer pgr.Stop()
			out = pgr.Writer
		}

		for {
			if c.Type() == db.CommitTypeGenesis {
				break
			}

			if c.Original.Equals(r.Original) {
				inRemote = true
			}

			initialCommit, err := c.InitalCommit(d.Noms())

			getStatus := func() (r string, mergedTime time.Time) {
				if inRemote {
					r = "MERGED"
				} else {
					r = "PENDING"
				}

				switch c.Type() {
				case db.CommitTypeReorder:
					r += " (REBASE)"
					mergedTime = c.Meta.Reorder.Date.Time
				case db.CommitTypeTx:
					mergedTime = c.Meta.Tx.Date.Time
				default:
					chk.Fail("unexpected commit type")
				}

				return
			}

			getTx := func() string {
				args := []string{}
				it := initialCommit.Meta.Tx.Args.Iterator()
				for {
					v := it.Next()
					if v == nil {
						break
					}
					if v.Kind() == types.BlobKind {
						args = append(args, fmt.Sprintf("blob(%s)", v.Hash().String()))
					} else {
						args = append(args, types.EncodedValue(v))
					}
				}
				return fmt.Sprintf("%s(%s)", initialCommit.Meta.Tx.Name, strings.Join(args, ", "))
			}

			fmt.Fprintln(out, color("commit "+c.Original.Hash().String(), "red+h"))
			table := (&tbl.Table{}).
				Add("Created: ", initialCommit.Meta.Tx.Date.String())

			status, t := getStatus()
			table.Add("Status: ", status)
			if t != (time.Time{}) {
				table.Add("Merged: ", t.String())
			}

			if !initialCommit.Original.Equals(c.Original) {
				initialBasis, err := initialCommit.Basis(d.Noms())
				if err != nil {
					return err
				}
				table.Add("Initial Basis: ", initialBasis.Original.Hash().String())
			}
			table.Add("Transaction: ", getTx())

			_, err = table.WriteTo(out)
			if err != nil {
				return err
			}

			basis, err := c.Basis(d.Noms())
			if err != nil {
				return err
			}

			err = diff.PrintDiff(out, basis.Data(d.Noms()), c.Data(d.Noms()), false)
			if err != nil {
				return err
			}

			fmt.Fprintln(out, "")
			c = basis
		}

		return nil
	})
}

func color(text, color string) string {
	if outputpager.IsStdoutTty() {
		return ansi.Color(text, color)
	}
	return text
}
