package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"time"

	"talebook/booksvr/internal/library"
	"talebook/booksvr/internal/sourcefile"
)

type sourceProcessRequest struct {
	Version     int      `json:"version"`
	DBPath      string   `json:"db"`
	LibraryRoot string   `json:"library_root"`
	Roots       []string `json:"roots"`
}
type sourceProcessMessage struct {
	Type        string                    `json:"type"`
	Path        string                    `json:"path,omitempty"`
	Result      *library.SourceSyncResult `json:"result,omitempty"`
	Directories []string                  `json:"directories,omitempty"`
}

// RunSourceSyncWorker is a private stdin/stdout protocol, never an HTTP service.
// Opening an existing DB avoids migrations, duplicate services and new tasks.
func RunSourceSyncWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	var request sourceProcessRequest
	reader := bufio.NewReader(in)
	var line bytes.Buffer
	for {
		part, more, e := reader.ReadLine()
		if e != nil {
			return e
		}
		if line.Len()+len(part) > 1<<20 {
			return fmt.Errorf("source worker request too large")
		}
		line.Write(part)
		if !more {
			break
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(line.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if request.Version != 1 || request.DBPath == "" || len(request.Roots) == 0 {
		return fmt.Errorf("invalid source worker request")
	}
	if err := sourcefile.Init(); err != nil {
		return err
	}
	db, err := openExistingDB(request.DBPath, 30000)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	encoder := json.NewEncoder(out)
	acknowledgements := json.NewDecoder(reader)
	result, err := library.SyncSourceFiles(ctx, db, request.Roots, request.LibraryRoot, library.SourceSyncOptions{FullDiscovery: true, OmitFilePaths: true, DirectoryVisited: func(path string) error {
		if e := encoder.Encode(sourceProcessMessage{Type: "directory", Path: path}); e != nil {
			return e
		}
		var ack struct {
			Type string `json:"type"`
		}
		if e := acknowledgements.Decode(&ack); e != nil {
			return e
		}
		if ack.Type != "continue" {
			return fmt.Errorf("invalid source worker acknowledgement")
		}
		return nil
	}})
	if err != nil {
		return err
	}
	return encoder.Encode(sourceProcessMessage{Type: "result", Result: &result, Directories: result.Directories})
}

type sourceProcessErrors struct{ data []byte }

func (w *sourceProcessErrors) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 8192 - len(w.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

func runFullSourceProcess(ctx context.Context, command []string, request sourceProcessRequest, visited func(string) error) (library.SourceSyncResult, error) {
	var result library.SourceSyncResult
	payload, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	if len(payload) > 1<<20 {
		return result, fmt.Errorf("source worker request too large")
	}
	if len(command) == 0 {
		return result, fmt.Errorf("source worker executable is missing")
	}
	if runtime.GOOS == "linux" {
		if nice, e := exec.LookPath("nice"); e == nil {
			command = append([]string{nice, "-n", "10"}, command...)
		}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = append(cmd.Environ(), "GOMAXPROCS=1")
	cmd.WaitDelay = 5 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	defer input.Close()
	var errors sourceProcessErrors
	cmd.Stderr = &errors
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	if err = cmd.Start(); err != nil {
		return result, err
	}
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	if _, err = input.Write(append(payload, '\n')); err != nil {
		return result, err
	}
	acknowledgements := json.NewEncoder(input)
	decoder := json.NewDecoder(pipe)
	decoder.DisallowUnknownFields()
	complete := false
	for {
		var message sourceProcessMessage
		if err = decoder.Decode(&message); err == io.EOF {
			break
		} else if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, fmt.Errorf("source worker protocol: %w", err)
		}
		switch message.Type {
		case "directory":
			if complete || message.Path == "" {
				return result, fmt.Errorf("invalid source worker directory event")
			}
			if visited != nil {
				if err = visited(message.Path); err != nil {
					return result, err
				}
			}
			if err = acknowledgements.Encode(struct {
				Type string `json:"type"`
			}{"continue"}); err != nil {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				return result, err
			}
		case "result":
			if complete || message.Result == nil {
				return result, fmt.Errorf("invalid source worker result")
			}
			result = *message.Result
			result.Directories = message.Directories
			complete = true
		default:
			return result, fmt.Errorf("unknown source worker message %q", message.Type)
		}
	}
	err = cmd.Wait()
	waited = true
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, fmt.Errorf("source worker: %w: %s", err, errors.data)
	}
	if !complete {
		return result, fmt.Errorf("source worker exited without a result")
	}
	return result, nil
}
