package ftp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/ftpserverlib"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/blob"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/config"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/fileops"
)

func TestFTPSTORRenameAndDeleteUseSharedFileCommands(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewTreeLocal(filepath.Join(t.TempDir(), "metadata"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewLocal(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })

	controlPort := reserveTCPPort(t)
	passivePort := reserveTCPPort(t)
	cfg := config.Config{
		FTPPort:    controlPort,
		FTPUser:    "test-user",
		FTPPass:    "test-pass",
		FTPPasvURL: "127.0.0.1",
		FTPPasvMin: passivePort,
		FTPPasvMax: passivePort,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	commands := fileops.New(store, objects, objects)
	server := ftpserver.NewFtpServer(NewMainDriverWithCommands(cfg, store, objects, logger, commands, time.Second))
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	t.Cleanup(func() {
		_ = server.Stop()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Error("FTP server did not stop")
		}
	})

	controlConn := dialFTPControl(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)))
	t.Cleanup(func() { _ = controlConn.Close() })
	control := bufio.NewReadWriter(bufio.NewReader(controlConn), bufio.NewWriter(controlConn))
	readFTPCode(t, control, 220)
	writeFTPCommand(t, control, "USER test-user")
	readFTPCode(t, control, 331)
	writeFTPCommand(t, control, "PASS test-pass")
	readFTPCode(t, control, 230)
	writeFTPCommand(t, control, "MKD /docs")
	readFTPCode(t, control, 257)

	writeFTPCommand(t, control, "PASV")
	pasvResponse := readFTPCode(t, control, 227)
	dataAddress := passiveAddress(t, pasvResponse)
	dataConn, err := net.DialTimeout("tcp", dataAddress, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	writeFTPCommand(t, control, "STOR /docs/a.txt")
	readFTPCode(t, control, 150)
	if _, err = dataConn.Write([]byte("shared semantics")); err != nil {
		t.Fatal(err)
	}
	if err = dataConn.Close(); err != nil {
		t.Fatal(err)
	}
	readFTPCode(t, control, 226)

	writeFTPCommand(t, control, "RNFR /docs/a.txt")
	readFTPCode(t, control, 350)
	writeFTPCommand(t, control, "RNTO /docs/b.txt")
	readFTPCode(t, control, 250)
	record, found, err := store.Find(ctx, "docs/b.txt")
	if err != nil || !found {
		t.Fatalf("renamed mapping found=%t err=%v", found, err)
	}

	writeFTPCommand(t, control, "DELE /docs/b.txt")
	readFTPCode(t, control, 250)
	if _, found, err = store.Find(ctx, "docs/b.txt"); err != nil || found {
		t.Fatalf("active mapping found=%t err=%v", found, err)
	}
	trash, err := store.ListTrash(ctx)
	if err != nil || len(trash) != 1 || trash[0].PhysicalHash != record.PhysicalHash {
		t.Fatalf("trash=%+v err=%v", trash, err)
	}
	reader, err := objects.NewReader(ctx, record.PhysicalHash)
	if err != nil {
		t.Fatalf("FTP DELE removed the restorable blob: %v", err)
	}
	_ = reader.Close()

	writeFTPCommand(t, control, "QUIT")
	readFTPCode(t, control, 221)
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func dialFTPControl(t *testing.T, address string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			return connection
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("FTP server did not start")
	return nil
}

func writeFTPCommand(t *testing.T, control *bufio.ReadWriter, command string) {
	t.Helper()
	if _, err := fmt.Fprintf(control, "%s\r\n", command); err != nil {
		t.Fatal(err)
	}
	if err := control.Flush(); err != nil {
		t.Fatal(err)
	}
}

func readFTPCode(t *testing.T, control *bufio.ReadWriter, expected int) string {
	t.Helper()
	line, err := control.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	line = strings.TrimSpace(line)
	if len(line) < 3 {
		t.Fatalf("invalid FTP response %q", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil || code != expected {
		t.Fatalf("FTP response=%q, want code %d", line, expected)
	}
	return line
}

func passiveAddress(t *testing.T, response string) string {
	t.Helper()
	start := strings.LastIndex(response, "(")
	end := strings.LastIndex(response, ")")
	if start < 0 || end <= start {
		t.Fatalf("invalid PASV response %q", response)
	}
	parts := strings.Split(response[start+1:end], ",")
	if len(parts) != 6 {
		t.Fatalf("invalid PASV response %q", response)
	}
	high, err := strconv.Atoi(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	low, err := strconv.Atoi(parts[5])
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort(strings.Join(parts[:4], "."), strconv.Itoa(high*256+low))
}
