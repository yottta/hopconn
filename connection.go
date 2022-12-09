package hopconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	// ErrAlreadyEstablished is returned when a new connection attempt is performed while a connection is already established.
	ErrAlreadyEstablished = errors.New("a connection was already established")
	// ErrStopped is returned when a new connection attempt is performed while the existing Manager is already stopped.
	ErrStopped = errors.New("connection manager stopped")
	// ErrNoConnectionEstablished is returned when the listening is failing.
	ErrNoConnectionEstablished = errors.New("no conn established")
	// ErrAddressesDismissed indicates that some addresses are dismissed from being attempted a connection to due to the misconfiguration of the connection.
	// In order to avoid this, be sure that you are using WithNoOfAddressesToAttempt to express the number of addresses that are needed to be attempted.
	ErrAddressesDismissed = errors.New("addresses dismissed because of the misconfigured connection")
)

// Manager defines the way a hole punch connection manager behaves.
type Manager interface {
	// AttemptConnection receives the addresses and schedules them for attempting.
	// IMPORTANT: This method can be called only once. After this, the client needs to use Errors() in conjunction with EstablishedEvents()
	// to listen for any events happening.
	AttemptConnection(addresses ...string) error
	// RegisterDataHandler registers methods that will be invoked anytime some data is received by the connection
	RegisterDataHandler(func(dat []byte))

	// Errors returns a read only channel that returns **termination** errors occurred during connection life span.
	// The errors returned will be returned from different flows:
	// * during listening for a connection
	// * during reading from the connection if occurred an error that closed the connection
	// * during writing to the connection if occurred an error that closed the connection
	// IMPORTANT: Once an error is sent through this channel that is an indication that the connection is not viable anymore and a new one needs to be created.
	Errors() <-chan error
	// EstablishedEvents returns a read only channel that just signals the success establishing of a connection.
	// Once the channel is closed means that the connection was established.
	EstablishedEvents() <-chan struct{}

	// Write schedules the given byte slice to be written to the connection.
	Write([]byte) error
	// Close is closing the connection and all associated resources.
	Close()

	// LocalAddress returns back the local address of the connection.
	// This can be controlled by using WithLocalIPProvider.
	LocalAddress() string
	// PublicAddress returns back the public address of the connection.
	// This can be controlled by using WithPublicIPProvider
	PublicAddress() string
}

type managerOpts struct {
	log zerolog.Logger

	attemptTimeout         time.Duration
	attemptRetries         int
	noOfAddressesToAttempt int

	publicIPProvider IPProvider
	localIPProvider  IPProvider
}

func WithLoggingTraceID(traceID string) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.log = c.log.With().Str("traceID", traceID).Logger()
	}
}

func WithLoggingLevel(l zerolog.Level) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.log = c.log.Level(l)
	}
}

func WithAttemptTimeout(t time.Duration) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.attemptTimeout = t
	}
}

func WithAttemptRetries(r int) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.attemptRetries = r
	}
}

func WithNoOfAddressesToAttempt(n int) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.noOfAddressesToAttempt = n
	}
}

func WithPublicIPProvider(p IPProvider) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.publicIPProvider = p
	}
}

func WithLocalIPProvider(p IPProvider) func(c *managerOpts) {
	return func(c *managerOpts) {
		c.localIPProvider = p
	}
}

type connectionManager struct {
	log zerolog.Logger

	localListener   net.Listener
	globalListener  net.Listener // we are using this just to reserve a port that will be used to run net.DialTCP
	publicIP        string
	publicPort      int
	localIPProvider IPProvider

	dataOutHandlers   []func([]byte)
	errorEvents       chan error
	establishedEvents chan struct{}

	stopCh    chan struct{}
	stoppedMu sync.RWMutex
	stopped   bool

	writeCh          chan []byte
	attemptAddresses chan string
	attemptRetries   int
	attemptTimeout   time.Duration
}

// NewConn returns a Manager. During calling this, the Manager will start listening on a system assigned port and will allocate also a
// port for being used when Manager.AttemptConnection calls.
// Even if the Manager.AttemptConnection will not be used, be sure that you are calling Manager.Close when you are done with it
// or that you are cancelling the ctx given to this constructor.
// This constructor builds a Manager that ensures correct behaviour of the Manager.AttemptConnection with a list of addresses up to a size equals with 3.
func NewConn(ctx context.Context, opts ...func(manager *managerOpts)) (Manager, error) {
	mOpts := &managerOpts{
		log:                    log.With().Logger().Level(zerolog.ErrorLevel),
		attemptTimeout:         3 * time.Second,
		attemptRetries:         3,
		noOfAddressesToAttempt: 3,
		publicIPProvider:       DefaultPublicIP,
		localIPProvider:        LocalIP,
	}
	for _, o := range opts {
		o(mOpts)
	}
	local, err := net.Listen("tcp", "")
	if err != nil {
		return nil, err
	}
	global, err := net.Listen("tcp", "")
	if err != nil {
		return nil, err
	}
	publicIP, err := mOpts.publicIPProvider()
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(global.Addr().String())
	if err != nil {
		return nil, err
	}

	portNo, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}

	manager := &connectionManager{
		log: mOpts.log,

		localListener:   local,
		globalListener:  global,
		publicIP:        publicIP,
		publicPort:      portNo,
		localIPProvider: mOpts.localIPProvider,

		stopped:   false,
		stoppedMu: sync.RWMutex{},

		stopCh:            make(chan struct{}, 1),
		errorEvents:       make(chan error, 3),
		establishedEvents: make(chan struct{}, 2),

		writeCh:          make(chan []byte, 100),
		attemptAddresses: make(chan string, mOpts.noOfAddressesToAttempt),
		attemptRetries:   mOpts.attemptRetries,
		attemptTimeout:   mOpts.attemptTimeout,
	}
	go func() {
		if err := manager.listen(ctx); err != nil {
			manager.log.Error().Err(err).Msg("listening ended with error")
			manager.Close()
			manager.sendError(err)
			close(manager.errorEvents)
			return
		}
	}()
	return manager, nil
}

// AttemptConnection gets the list of addresses and adds them to a channel.
// In case the len(peerAddresses) is greater than the number of addresses to be attempted configured during NewConn,
// this method does not ensure that all the addresses will be used.
func (c *connectionManager) AttemptConnection(peerAddresses ...string) error {
	if c.isStopped() {
		return ErrStopped
	}
	var notAdded []string
	for _, a := range peerAddresses {
		select {
		case c.attemptAddresses <- a:
		default:
			notAdded = append(notAdded, a)
		}
	}
	if len(notAdded) > 0 {
		return fmt.Errorf("%w: %s", ErrAddressesDismissed, strings.Join(notAdded, ","))
	}
	close(c.attemptAddresses)
	return nil
}

// LocalAddress makes use of the LocalIP in order to discover the current system localIP.
// In case that the call to LocalIP fails this will get only the address of the net.Listener in charge of accepting connections requests.
func (c *connectionManager) LocalAddress() string {
	ip, err := c.localIPProvider()
	if err != nil {
		return c.localListener.Addr().String()
	}
	_, port, err := net.SplitHostPort(c.localListener.Addr().String())
	if err != nil {
		return c.localListener.Addr().String()
	}
	return net.JoinHostPort(ip, port)
}

// PublicAddress returns the publicIP and publicPort generated during NewConn.
func (c *connectionManager) PublicAddress() string {
	return net.JoinHostPort(c.publicIP, strconv.Itoa(c.publicPort))
}

// Errors returns the read-only channel for the generated errors. See Manager.Errors for more details.
func (c *connectionManager) Errors() <-chan error {
	return c.errorEvents
}

// EstablishedEvents returns the read-only channel to signal when an event is established. See Manager.EstablishedEvents for more details.
func (c *connectionManager) EstablishedEvents() <-chan struct{} {
	return c.establishedEvents
}

// Write is used to write data on the established connection.
func (c *connectionManager) Write(in []byte) error {
	if c.isStopped() {
		return ErrStopped
	}
	c.writeCh <- in
	return nil
}

// RegisterDataHandler is used to register method handlers for processing incoming data. See Manager.RegisterDataHandler for details.
func (c *connectionManager) RegisterDataHandler(dh func(dat []byte)) {
	c.dataOutHandlers = append(c.dataOutHandlers, dh)
}

// Close is closing the current Manager. Once stopped, this method will do nothing.
func (c *connectionManager) Close() {
	if c.isStopped() {
		return
	}
	c.log.Trace().Msg("Close() called")
	close(c.stopCh)
	c.markStopped()
	return
}

func (c *connectionManager) listen(ctx context.Context) error {
	conn, err := c.establishConn(ctx)
	if err != nil {
		return err
	}
	close(c.establishedEvents)

	loopsCtx, cancel := context.WithCancel(ctx)
	go func() {
		<-c.stopCh
		cancel()
		if err := conn.Close(); err != nil {
			c.log.Warn().Err(err).
				Msg("failed to close connection during stopping the connection")
		}
		c.markStopped()
	}()

	go func() {
		var wgl sync.WaitGroup
		wgl.Add(1)
		go func() {
			defer func() {
				cancel()
				wgl.Done()
			}()
			if err := c.readLoop(loopsCtx, conn); err != nil {
				c.log.Warn().
					Err(err).
					Msg("error reading from connection")
				c.sendError(err)
			}
		}()
		wgl.Add(1)
		go func() {
			defer func() {
				cancel()
				wgl.Done()
			}()
			if err := c.writeLoop(loopsCtx, conn); err != nil {
				c.log.Warn().
					Err(err).
					Msg("error writing to connection")
				c.sendError(err)
			}
		}()
		wgl.Wait()
		c.Close()
		close(c.errorEvents)
	}()

	return nil
}

func (c *connectionManager) establishConn(ctx context.Context) (net.Conn, error) {
	established := make(chan connAttemptWrapper, 2)
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer func() {
			cancel()
		}()
		select {
		case <-c.stopCh:
			return
		case <-connectionCtx.Done():
			return
		}
	}()
	var wg, ewg sync.WaitGroup

	var conn net.Conn
	ewg.Add(1)
	go func() {
		defer ewg.Done()
		for caw := range established {
			if conn != nil {
				c.log.Info().
					Str("local_address", caw.conn.LocalAddr().String()).
					Str("remote_address", caw.conn.RemoteAddr().String()).
					Msg("closing another established connection as there is already an established conn")
				_ = caw.conn.Close()
				continue
			}
			conn = caw.conn
			c.log.Info().
				Str("local_address", caw.conn.LocalAddr().String()).
				Str("remote_address", caw.conn.RemoteAddr().String()).
				Msg("connection established")
			cancel()
			// do not break - consume the connection(s) inside the channel and close them since we already have one established connection
		}
	}()
	go func() {
		<-connectionCtx.Done()
		c.log.Trace().Msg("connectionCtx done")
		if err := c.localListener.Close(); err != nil {
			c.log.Error().Err(err).Msg("error closing local listener as the connection ctx was cancelled")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		accept, err := c.localListener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				c.log.Warn().Err(err).Msg("local listening failed")
			}
			return
		}
		c.log.Info().Msg("local listening accepted")

		established <- connAttemptWrapper{
			address: accept.RemoteAddr().String(),
			conn:    accept,
			err:     nil,
		}
	}()

	wg.Add(1)
	go func() {
		defer func() {
			wg.Done()
			cancel() // when all attempted addresses are done then it's nothing else to be done so close everything
			c.log.Trace().Msg("attempted addresses finished")
		}()
		if err := c.globalListener.Close(); err != nil { // close and reuse its port
			c.log.Debug().
				Err(err).
				Str("address", c.globalListener.Addr().String()).
				Msg("error closing global listener")
		}
		for {
			select {
			case <-connectionCtx.Done():
				return
			case peerAddress, ok := <-c.attemptAddresses:
				if !ok {
					c.log.Debug().Msg("no more addresses to check")
					return
				}
				c.log.Info().
					Str("peerAddress", peerAddress).
					Msg("trying new peer address")
				for i := 0; i < c.attemptRetries; i++ {
					err := func() error {
						d := net.Dialer{Timeout: c.attemptTimeout, LocalAddr: &net.TCPAddr{Port: c.publicPort}}
						accept, err := d.DialContext(connectionCtx, "tcp", peerAddress)
						if err != nil {
							return err
						}
						established <- connAttemptWrapper{
							address: peerAddress,
							conn:    accept,
							err:     nil,
						}
						return nil
					}()
					if err != nil {
						c.log.Warn().
							Err(err).
							Int("local_port", c.publicPort).
							Str("remote_address", peerAddress).
							Msg("error dialing peer")
						if errors.Is(err, context.Canceled) || errors.Is(err, syscall.EADDRINUSE) {
							break
						}
						continue
					}
					break
				}
			}
		}
	}()

	wg.Wait()
	close(established)

	ewg.Wait()
	if conn == nil {
		c.Close()
		return nil, ErrNoConnectionEstablished
	}

	return conn, nil
}

func (c *connectionManager) readLoop(ctx context.Context, conn net.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			in := make([]byte, 32)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := conn.Read(in)
			if err != nil {
				if os.IsTimeout(err) {
					continue
				}
				if errors.Is(err, io.EOF) {
					return err
				}
				if errors.Is(err, net.ErrClosed) {
					return err
				}
				c.log.Warn().
					Err(err).
					Msg("unexpected error occurred during reading message from the connection")
				continue
			}
			for _, ch := range c.dataOutHandlers {
				ch(in[:n])
			}
		}
	}
}

func (c *connectionManager) writeLoop(ctx context.Context, conn net.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case dat, ok := <-c.writeCh:
			if !ok {
				return nil
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(dat); err != nil {
				if os.IsTimeout(err) {
					continue
				}
				if errors.Is(err, io.EOF) {
					return err
				}
				if errors.Is(err, net.ErrClosed) {
					return err
				}
				c.log.Warn().
					Err(err).
					Msg("unexpected error occurred during write message to the connection")
				continue
			}
		}
	}
}

func (c *connectionManager) isStopped() bool {
	c.stoppedMu.RLock()
	defer c.stoppedMu.RUnlock()
	return c.stopped
}

func (c *connectionManager) markStopped() {
	if c.isStopped() {
		return
	}
	c.stoppedMu.Lock()
	c.stopped = true
	close(c.writeCh)
	c.stoppedMu.Unlock()
}

func (c *connectionManager) sendError(err error) {
	t := time.NewTimer(time.Second)
	select {
	case c.errorEvents <- err:
		if !t.Stop() {
			<-t.C
		}
	case <-t.C:
		c.log.Warn().Err(err).Msg("error discarded")
	}
}

type connAttemptWrapper struct {
	address string
	conn    net.Conn
	err     error
}

// ConnAttemptError is the error returned by the Manager in case the read loop is failing.
type ConnAttemptError struct {
	Address string
	Err     error
}

func (cae ConnAttemptError) Error() string {
	return fmt.Errorf("%w: %s", cae.Err, cae.Address).Error()
}

func (cae ConnAttemptError) Unwrap() error {
	return cae.Err
}
