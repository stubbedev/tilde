// Command lys-wmctl queries and controls a running lys-wm over its IPC socket.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const usage = `usage: lys-wmctl [--socket <path>] <command>

  dispatch <action> [args...]  run an action, as a keybind would (see docs/actions.md)
  reload                       read the config file again
  outputs                      print the outputs as JSON
  workspaces                   print the open workspaces as JSON
  windows                      print the mapped windows as JSON
  activewindow                 print the focused window as JSON, or null
  version                      print the IPC protocol version
  watch                        print events as JSON lines until lys-wm exits

The socket is --socket, else $LYS_WM_SOCKET, else the only socket in
$XDG_RUNTIME_DIR/lys-wm.
`

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "lys-wmctl:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("bad usage")

var queries = []ipc.Kind{ipc.KindOutputs, ipc.KindWorkspaces, ipc.KindWindows, ipc.KindActiveWindow, ipc.KindVersion, ipc.KindWatch}

func parseArgs(args []string) (ipc.Request, error) {
	if len(args) == 0 {
		return ipc.Request{}, fmt.Errorf("%w: no command", errUsage)
	}
	cmd, rest := args[0], args[1:]
	var req ipc.Request
	switch kind := ipc.Kind(cmd); {
	case kind == ipc.KindDispatch:
		if len(rest) == 0 {
			return ipc.Request{}, fmt.Errorf("%w: dispatch needs an action", errUsage)
		}
		return ipc.Request{Request: ipc.KindDispatch, Action: strings.Join(rest, " ")}, nil
	case cmd == "reload":
		req = ipc.Request{Request: ipc.KindDispatch, Action: wm.ReloadAction{}.String()}
	case slices.Contains(queries, kind):
		req = ipc.Request{Request: kind}
	default:
		return ipc.Request{}, fmt.Errorf("%w: unknown command %q", errUsage, cmd)
	}
	if len(rest) != 0 {
		return ipc.Request{}, fmt.Errorf("%w: %s takes no arguments", errUsage, cmd)
	}
	return req, nil
}

func watch(path string, out io.Writer) (err error) {
	stream, err := ipc.Subscribe(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	for {
		line, err := stream.Next()
		if err != nil {
			return err
		}
		if _, err := out.Write(line); err != nil {
			return err
		}
	}
}

func run(args []string, getenv func(string) string, out io.Writer) error {
	flags := flag.NewFlagSet("lys-wmctl", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	req, err := parseArgs(flags.Args())
	if err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	path := *socket
	if path == "" {
		if path, err = ipc.FindSocket(getenv); err != nil {
			return err
		}
	}
	if req.Request == ipc.KindWatch {
		return watch(path, out)
	}
	result, err := ipc.Do(path, req)
	if err != nil || req.Request == ipc.KindDispatch {
		return err
	}
	if result == nil {
		result = json.RawMessage("null")
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, result, "", "  "); err != nil {
		return fmt.Errorf("format the result: %w", err)
	}
	pretty.WriteByte('\n')
	_, err = out.Write(pretty.Bytes())
	return err
}
