package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"talebook/booksvr/internal/scan"
	"time"
)

// Read-only inspection makes extraction issues reproducible without touching a DB.
func runMetadataInspect(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("metadata-inspect", flag.ContinueOnError)
	fs.SetOutput(out)
	file := fs.String("file", "", "源文件路径")
	format := fs.String("format", "", "格式，默认使用扩展名")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("metadata-inspect requires -file")
	}
	if *format == "" {
		*format = strings.TrimPrefix(filepath.Ext(*file), ".")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m, err := scan.ReadMetadata(ctx, *file, *format)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return enc.Encode(m)
}
