package proxy

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	// ErrGroupClosed is returned by the *Group.ListenAndServe
	// method after a call to *Group.Close.
	ErrGroupClosed = errors.New("proxy group closed")

	// ErrDuplicatedStream is returned by the
	// *Group.ListenAndServe method when it is called with
	// duplicated streams.
	ErrDuplicatedStream = errors.New("duplicated stream")
)

// Group represents a group of proxy servers.
type Group struct {
	// BeforeAccept is called for every proxy once its listener is
	// ready, just before accepting connections.
	BeforeAccept func() error

	inClose atomic.Bool

	mu      sync.Mutex
	proxies map[Stream]*Proxy

	proxiesGroup sync.WaitGroup
}

// ListenAndServe established the specified data streams.
func (pg *Group) ListenAndServe(streams []Stream) error {
	if pg.closing() {
		return ErrGroupClosed
	}

	if err := validateStreams(streams); err != nil {
		return fmt.Errorf("stream validation: %w", err)
	}

	errc := make(chan error)
	go func() {
		var wg sync.WaitGroup
		for _, stream := range streams {
			stream := stream
			wg.Add(1)
			go func() {
				defer wg.Done()
				errc <- pg.handleStream(stream)
			}()
		}
		wg.Wait()
		close(errc)
	}()

	var errs []error
	for err := range errc {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return ErrGroupClosed
}

func (pg *Group) handleStream(stream Stream) error {
	p := &Proxy{}

	if err := pg.trackProxy(stream, p, true); err != nil {
		return err
	}
	defer pg.trackProxy(stream, p, false) //nolint:errcheck

	p.BeforeAccept = pg.BeforeAccept
	if err := p.ListenAndServe(stream.listenNetwork, stream.listenAddr, stream.dialNetwork, stream.dialAddr); !errors.Is(err, ErrProxyClosed) {
		return err
	}
	return nil
}

func (pg *Group) trackProxy(stream Stream, p *Proxy, add bool) error {
	pg.mu.Lock()
	defer pg.mu.Unlock()

	if pg.proxies == nil {
		pg.proxies = make(map[Stream]*Proxy)
	}

	if add {
		if pg.closing() {
			return ErrGroupClosed
		}
		pg.proxies[stream] = p
		pg.proxiesGroup.Add(1)
	} else {
		delete(pg.proxies, stream)
		pg.proxiesGroup.Done()
	}
	return nil
}

// Close closes all the established data streams.
func (pg *Group) Close() error {
	pg.inClose.Store(true)
	err := pg.closeProxies()
	pg.proxiesGroup.Wait()
	return err
}

func (pg *Group) closeProxies() error {
	pg.mu.Lock()
	defer pg.mu.Unlock()

	var errs []error
	for s, p := range pg.proxies {
		if err := p.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing proxy: %v: %v", s, err))
		}
		delete(pg.proxies, s)
	}
	return errors.Join(errs...)
}

func (pg *Group) closing() bool {
	return pg.inClose.Load()
}

// Stream represents a bidirectional data stream.
type Stream struct {
	listenNetwork, listenAddr string
	dialNetwork, dialAddr     string
}

// ParseStream parses a string representing a bidirectional data
// stream with the format <listener>,<target>.
func ParseStream(s string) (Stream, error) {
	sides := strings.Split(s, ",")
	if len(sides) != 2 {
		return Stream{}, fmt.Errorf("malformed stream %q", s)
	}

	listenNetwork, listenAddr, err := parseAddr(sides[0])
	if err != nil {
		return Stream{}, fmt.Errorf("malformed listen side %q: %w", sides[0], err)
	}

	dialNetwork, dialAddr, err := parseAddr(sides[1])
	if err != nil {
		return Stream{}, fmt.Errorf("malformed dial side %q: %w", sides[1], err)
	}

	stream := Stream{
		listenNetwork: listenNetwork,
		listenAddr:    listenAddr,
		dialNetwork:   dialNetwork,
		dialAddr:      dialAddr,
	}

	return stream, nil
}

func parseAddr(s string) (network, addr string, err error) {
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", fmt.Errorf("malformed address")
	}

	network = s[:i]
	if network == "" {
		return "", "", errors.New("empty network")
	}

	addr = s[i+1:]
	if addr == "" {
		return "", "", errors.New("empty address")
	}

	return network, addr, nil
}

func validateStreams(streams []Stream) error {
	for i := 0; i < len(streams); i++ {
		for j := i + 1; j < len(streams); j++ {
			if streams[i] == streams[j] {
				return ErrDuplicatedStream
			}
		}
	}
	return nil
}

// String returns the string representation of the stream.
func (stream Stream) String() string {
	return fmt.Sprintf("%v:%v,%v:%v", stream.listenNetwork, stream.listenAddr, stream.dialNetwork, stream.dialAddr)
}