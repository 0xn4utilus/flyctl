package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/cmdctx"
	"github.com/superfly/flyctl/internal/wireguard"
	"github.com/superfly/flyctl/pkg/wg"
)

var (
	ErrCantBind = errors.New("can't bind agent socket")
)

type Server struct {
	listener *net.UnixListener
	ctx      context.Context
	tunnels  map[string]*wg.Tunnel
	client   *api.Client
	cmdctx   *cmdctx.CmdContext
	lock     sync.Mutex
}

type handlerFunc func(net.Conn, []string) error

func (s *Server) handle(c net.Conn) {
	defer c.Close()

	buf, err := read(c)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			log.Printf("couldn't read command: %s", err)
		}
		return
	}

	args := strings.Split(string(buf), " ")

	cmds := map[string]handlerFunc{
		"kill":      s.handleKill,
		"ping":      s.handlePing,
		"connect":   s.handleConnect,
		"establish": s.handleEstablish,
	}

	handler, ok := cmds[args[0]]
	if !ok {
		s.errLog(c, "bad command: %v", args)
		return
	}

	if err = handler(c, args); err != nil {
		s.errLog(c, "err handling %s: %s", args[0], err)
		return
	}
}

func NewServer(path string, ctx *cmdctx.CmdContext) (*Server, error) {
	if c, err := NewClient(path); err == nil {
		c.Kill()
	}

	if err := removeSocket(path); err != nil {
		// most of these errors just mean the socket isn't already there
		// which is what we want.

		if errors.Is(err, ErrCantBind) {
			return nil, err
		}
	}

	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		fmt.Printf("Failed to resolve: %v\n", err)
		os.Exit(1)
	}

	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("can't bind: %w", err)
	}

	l.SetUnlinkOnClose(true)

	s := &Server{
		listener: l,
		cmdctx:   ctx,
		client:   ctx.Client.API(),
	}

	return s, nil
}

func DefaultServer(ctx *cmdctx.CmdContext) (*Server, error) {
	return NewServer(fmt.Sprintf("%s/.fly/fly-agent.sock", os.Getenv("HOME")), ctx)
}

func (s *Server) Serve() {
	defer s.listener.Close()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// this can't really be how i'm supposed to do this
			if strings.Contains(err.Error(), "use of closed network connection") {
				return
			}

			log.Printf("warning: couldn't accept connection: %s", err)
			continue
		}

		go s.handle(conn)
	}
}

func (s *Server) errLog(c net.Conn, format string, args ...interface{}) {
	writef(c, "err "+format, args...)
	log.Printf(format, args...)
}

func (s *Server) copy(dst net.Conn, src io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	io.Copy(dst, src)
}

func (s *Server) handleKill(c net.Conn, args []string) error {
	s.listener.Close()
	return nil
}

func (s *Server) handlePing(c net.Conn, args []string) error {
	return writef(c, "pong %d", os.Getpid())
}

func (s *Server) handleEstablish(c net.Conn, args []string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if len(args) != 2 {
		return fmt.Errorf("malformed establish command")
	}

	orgs, err := s.client.GetOrganizations()
	if err != nil {
		return fmt.Errorf("can't load organizations from config: %s", err)
	}

	var org *api.Organization
	for _, o := range orgs {
		if o.Slug == args[1] {
			org = &o
		}
	}

	if org == nil {
		return fmt.Errorf("no such organization")
	}

	if _, ok := s.tunnels[org.Slug]; ok {
		return writef(c, "ok")
	}

	state, err := wireguard.StateForOrg(s.client, org, "", "")
	if err != nil {
		return fmt.Errorf("can't get wireguard state for %s: %s", org.Slug, err)
	}

	tunnel, err := wg.Connect(*state.TunnelConfig())
	if err != nil {
		return fmt.Errorf("can't connect wireguard: %w", err)
	}

	s.tunnels[org.Slug] = tunnel
	return writef(c, "ok")
}

func (s *Server) handleConnect(c net.Conn, args []string) error {
	log.Printf("incoming connect: %v", args)

	if len(args) < 2 || len(args) > 3 {
		return fmt.Errorf("malformed connect command: %v", args)
	}

	d := net.Dialer{}

	if len(args) > 2 {
		timeout, err := strconv.ParseUint(args[2], 10, 32)
		if err != nil {
			return fmt.Errorf("invalid timeout: %s", err)
		}

		d.Timeout = time.Duration(timeout) * time.Millisecond
	}

	outconn, err := d.Dial("tcp", args[1])
	if err != nil {
		return fmt.Errorf("connection failed: %s", err)
	}

	defer outconn.Close()

	writef(c, "ok")

	wg := &sync.WaitGroup{}
	wg.Add(2)
	go s.copy(c, outconn, wg)
	go s.copy(outconn, c, wg)
	wg.Wait()

	return nil
}

func (s *Server) tunnelFor(slug string) (*wg.Tunnel, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	tunnel, ok := s.tunnels[slug]
	if !ok {
		return nil, fmt.Errorf("no tunnel for %s established", slug)
	}

	return tunnel, nil
}

func resolve(tunnel *wg.Tunnel, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}

	if n := net.ParseIP(host); n != nil && n.To16() != nil {
		return fmt.Sprintf("[%s]:%s", n, port), nil
	}

	addrs, err := tunnel.Resolver().LookupHost(context.Background(),
		host)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("[%s]:%s", addrs[0], port), nil
}

func removeSocket(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}

	if stat.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: refusing to remove something that isn't a socket", ErrCantBind)
	}

	return os.Remove(path)
}
