package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FastCGI record types and roles (FastCGI specification 1.0).
const (
	fcgiVersion      = 1
	fcgiBeginRequest = 1
	fcgiEndRequest   = 3
	fcgiParams       = 4
	fcgiStdin        = 5
	fcgiStdout       = 6
	fcgiStderr       = 7
	fcgiResponder    = 1
	fcgiMaxContent   = 65535
	fcgiRequestID    = 1
)

func writeRecord(w io.Writer, typ byte, content []byte) error {
	for {
		chunk := content
		if len(chunk) > fcgiMaxContent {
			chunk = content[:fcgiMaxContent]
		}
		padding := byte((8 - len(chunk)%8) % 8)
		header := []byte{fcgiVersion, typ, 0, fcgiRequestID, 0, 0, padding, 0}
		binary.BigEndian.PutUint16(header[4:6], uint16(len(chunk)))
		if _, err := w.Write(header); err != nil {
			return err
		}
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		if _, err := w.Write(make([]byte, padding)); err != nil {
			return err
		}
		content = content[len(chunk):]
		if len(content) == 0 {
			return nil
		}
	}
}

func encodeLength(b *bytes.Buffer, n int) {
	if n < 128 {
		b.WriteByte(byte(n))
		return
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(n)|1<<31)
	b.Write(buf[:])
}

func encodeParams(params map[string]string) []byte {
	var b bytes.Buffer
	for k, v := range params {
		encodeLength(&b, len(k))
		encodeLength(&b, len(v))
		b.WriteString(k)
		b.WriteString(v)
	}
	return b.Bytes()
}

// fcgiResolve finds the script for a request. Existing .php files run as
// themselves, other existing files are served statically, and everything
// else goes to the front controller, as PHP frameworks expect.
func fcgiResolve(root, index, urlPath string) (script string, static bool) {
	clean := path.Clean("/" + urlPath)
	full := filepath.Join(root, filepath.FromSlash(clean))
	if fi, err := os.Stat(full); err == nil {
		if fi.IsDir() {
			if _, err := os.Stat(filepath.Join(full, index)); err == nil {
				return path.Join(clean, index), false
			}
		} else if strings.HasSuffix(clean, ".php") {
			return clean, false
		} else {
			return clean, true
		}
	}
	return "/" + index, false
}

// serveFastCGI sends the request to a FastCGI server and copies the response.
func (s *Server) serveFastCGI(w http.ResponseWriter, r *http.Request, route Route) {
	h := route.Handler
	index := h.Index
	if index == "" {
		index = "index.php"
	}
	script, static := fcgiResolve(h.Root, index, r.URL.Path)
	if static {
		s.serveStatic(w, r, h.Root)
		return
	}
	up := s.pick(route)
	timeout := 30 * time.Second
	if h.WakeTimeout > timeout {
		timeout = h.WakeTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, up.Network, up.Address)
	if err != nil {
		s.badGateway(w, route, err)
		return
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline.Add(5 * time.Minute))
	}

	host, port, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = r.Host
	}
	remoteHost, remotePort, _ := net.SplitHostPort(r.RemoteAddr)
	params := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"SERVER_SOFTWARE":   "racksail",
		"SERVER_PROTOCOL":   r.Proto,
		"SERVER_NAME":       host,
		"SERVER_PORT":       port,
		"REQUEST_METHOD":    r.Method,
		"REQUEST_URI":       r.URL.RequestURI(),
		"QUERY_STRING":      r.URL.RawQuery,
		"DOCUMENT_ROOT":     h.Root,
		"SCRIPT_NAME":       script,
		"SCRIPT_FILENAME":   filepath.Join(h.Root, filepath.FromSlash(script)),
		"REMOTE_ADDR":       remoteHost,
		"REMOTE_PORT":       remotePort,
		"CONTENT_TYPE":      r.Header.Get("Content-Type"),
		"CONTENT_LENGTH":    "",
	}
	if r.ContentLength >= 0 {
		params["CONTENT_LENGTH"] = strconv.FormatInt(r.ContentLength, 10)
	}
	if r.TLS != nil {
		params["HTTPS"] = "on"
	}
	for k, vs := range r.Header {
		if k == "Proxy" {
			// Never pass Proxy as HTTP_PROXY (httpoxy).
			continue
		}
		params["HTTP_"+strings.ReplaceAll(strings.ToUpper(k), "-", "_")] = strings.Join(vs, ", ")
	}

	begin := []byte{0, fcgiResponder, 0, 0, 0, 0, 0, 0}
	if err := writeRecord(conn, fcgiBeginRequest, begin); err != nil {
		s.badGateway(w, route, err)
		return
	}
	if err := writeRecord(conn, fcgiParams, encodeParams(params)); err != nil {
		s.badGateway(w, route, err)
		return
	}
	if err := writeRecord(conn, fcgiParams, nil); err != nil {
		s.badGateway(w, route, err)
		return
	}
	var stdin sync.WaitGroup
	stdin.Add(1)
	go func() {
		defer stdin.Done()
		buf := make([]byte, 32<<10)
		for {
			n, readErr := r.Body.Read(buf)
			if n > 0 {
				if err := writeRecord(conn, fcgiStdin, buf[:n]); err != nil {
					return
				}
			}
			if readErr != nil {
				// An empty record ends the input; if the connection is gone,
				// reading the response reports it.
				if err := writeRecord(conn, fcgiStdin, nil); err != nil {
					s.Log.Debug("fastcgi input interrupted", "route", route.ID, "error", err)
				}
				return
			}
		}
	}()

	stdout, done := fcgiStream(conn, s)
	// The request body must not be read after the handler returns, and the
	// demultiplexer must not be left blocked on an unread pipe.
	defer func() {
		stdout.Close()
		conn.Close()
		<-done
		stdin.Wait()
	}()
	resp := bufio.NewReader(stdout)
	headers, err := textproto.NewReader(resp).ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		s.badGateway(w, route, err)
		return
	}
	status := http.StatusOK
	if st := headers.Get("Status"); st != "" {
		if code, err := strconv.Atoi(strings.Fields(st)[0]); err == nil {
			status = code
		}
		headers.Del("Status")
	} else if headers.Get("Location") != "" {
		status = http.StatusFound
	}
	for k, vs := range headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, resp); err != nil {
		s.Log.Debug("fastcgi response interrupted", "route", route.ID, "error", err)
	}
}

// fcgiStream demultiplexes the records of a response: stdout goes to the
// returned reader, stderr to the log.
func fcgiStream(conn net.Conn, s *Server) (*io.PipeReader, <-chan struct{}) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var header [8]byte
		for {
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				pw.CloseWithError(err)
				return
			}
			length := int(binary.BigEndian.Uint16(header[4:6]))
			content := make([]byte, length+int(header[6]))
			if _, err := io.ReadFull(conn, content); err != nil {
				pw.CloseWithError(err)
				return
			}
			content = content[:length]
			switch header[1] {
			case fcgiStdout:
				if length > 0 {
					if _, err := pw.Write(content); err != nil {
						return
					}
				}
			case fcgiStderr:
				if length > 0 {
					s.Log.Warn("fastcgi stderr", "message", strings.TrimSpace(string(content)))
				}
			case fcgiEndRequest:
				pw.Close()
				return
			default:
				pw.CloseWithError(fmt.Errorf("fastcgi: unexpected record type %d", header[1]))
				return
			}
		}
	}()
	return pr, done
}
