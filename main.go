package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"github.com/fatih/color"
	"golang.org/x/term"
)

var host = flag.String("host", "", "ssh host")
var tag = flag.String("tag", "", "image tag")

func init() {
	flag.Parse()
	if *host == "" || *tag == "" {
		log.Println("host or tag is empty")
		os.Exit(2)
	}
}

func execCommand(c *exec.Cmd) error {
	color.Cyan("$ %s", strings.Join(c.Args, " "))

	// ↓-- https://github.com/creack/pty#shell --↓
	// Start the command with a pty.
	ptmx, err := pty.Start(c)
	if err != nil {
		return err
	}
	// Make sure to close the pty at the end.
	defer func() { _ = ptmx.Close() }() // Best effort.

	// Handle pty size.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH                        // Initial resize.
	defer func() { signal.Stop(ch); close(ch) }() // Cleanup signals when done.

	// Set stdin in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }() // Best effort.

	// Copy stdin to the pty and the pty to stdout.
	// NOTE: The goroutine will keep reading until the next keystroke before returning.
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	_, _ = io.Copy(os.Stdout, ptmx)

	return nil
}

func createTarball(ctx context.Context, tempDir string) error {
	path := filepath.Join(tempDir, "ctx.tar.gz")

	cmd := exec.CommandContext(ctx, "tar", "-czf", path, ".")

	return execCommand(cmd)
}

func buildFromTarballRemotely(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "ssh", "-t", "-R", "50218:localhost:8080", *host, "docker", "build", "-t", *tag, "http://127.0.0.1:50218/ctx.tar.gz")

	return execCommand(cmd)
}

func copyImageFromRemote(ctx context.Context, tempDir string) error {
	cmd := exec.CommandContext(ctx, "ssh", *host, "docker", "save", *tag)

	f, err := os.Create(filepath.Join(tempDir, "image.tar"))
	if err != nil {
		return err
	}

	cmd.Stdout = f
	cmd.Stderr = os.Stderr

	color.Cyan("$ %s > %s", strings.Join(cmd.Args, " "), filepath.Join(tempDir, "image.tar"))

	return cmd.Run()
}

func loadCopiedImageTarball(ctx context.Context, tempDir string) error {
	cmd := exec.CommandContext(ctx, "docker", "load", "-i", filepath.Join(tempDir, "image.tar"))

	return execCommand(cmd)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tempDir, err := os.MkdirTemp("/tmp", "docker-ssh-build-*")
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	if err := createTarball(ctx, tempDir); err != nil {
		log.Println(err)
		os.Exit(1)
	}

	go func() {
		if err := http.ListenAndServe("127.0.0.1:8080", http.FileServer(http.Dir(tempDir))); err != nil {
			log.Println(err)
			os.Exit(1)
		}
	}()

	// TODO: check if server started

	if err := buildFromTarballRemotely(ctx); err != nil {
		log.Println(err)
		os.Exit(1)
	}

	if err := copyImageFromRemote(ctx, tempDir); err != nil {
		log.Println(err)
		os.Exit(1)
	}

	if err := loadCopiedImageTarball(ctx, tempDir); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
