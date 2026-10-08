package command

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// listenHerdr starts a one-request-per-connection Unix socket standing in for
// the Herdr server: it records each request line and answers it with result
// under the request's own id. Darwin caps sun_path at 104 bytes, so the
// socket lives under a short temporary directory, not t.TempDir().
func listenHerdr(t *testing.T, result string) (path string, requests func() []map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path = filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan map[string]any, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var req map[string]any
			_ = json.Unmarshal(line, &req)
			got <- req
			id, _ := req["id"].(string)
			_, _ = conn.Write([]byte(`{"id":` + strconv.Quote(id) + `,"result":` + result + "}\n"))
			_ = conn.Close()
		}
	}()
	return path, func() []map[string]any {
		var all []map[string]any
		for {
			select {
			case req := <-got:
				all = append(all, req)
			default:
				return all
			}
		}
	}
}

// TestPopupCmd_OpensThePluginPaneOverTheSocket proves `shep popup` (the
// plugin's open action) opens the picker with one plugin.pane.open request,
// the one `herdr plugin pane open --placement popup` sends, without reading
// any config.
func TestPopupCmd_OpensThePluginPaneOverTheSocket(t *testing.T) {
	socket, requests := listenHerdr(t, `{"type":"plugin_pane_opened"}`)
	t.Setenv("HERDR_SOCKET_PATH", socket)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "absent"))
	var out, errOut bytes.Buffer
	if err := New(WithStreams(&out, &errOut)).executeArgs([]string{"popup", "--plugin", "tranceh2.shep", "--entrypoint", "picker"}); err != nil {
		t.Fatalf("popup: %v (stderr %q)", err, errOut.String())
	}
	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(reqs))
	}
	want := map[string]any{"plugin_id": "tranceh2.shep", "entrypoint": "picker", "placement": "popup", "focus": true}
	if reqs[0]["method"] != "plugin.pane.open" || !reflect.DeepEqual(reqs[0]["params"], want) {
		t.Errorf("request = %v, want plugin.pane.open %v", reqs[0], want)
	}
}

// TestPopupCmd_WithoutASocketFailsOnOneLine proves the action refuses with a
// single stderr line when Herdr did not provide its socket.
func TestPopupCmd_WithoutASocketFailsOnOneLine(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	var out, errOut bytes.Buffer
	err := New(WithStreams(&out, &errOut)).executeArgs([]string{"popup", "--plugin", "tranceh2.shep", "--entrypoint", "picker"})
	if err == nil {
		t.Fatal("popup succeeded without a socket")
	}
	if lines := strings.Split(strings.TrimSpace(errOut.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "HERDR_SOCKET_PATH") {
		t.Errorf("stderr = %q, want one line naming HERDR_SOCKET_PATH", errOut.String())
	}
}
