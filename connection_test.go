package hopconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewConn(t *testing.T) {
	t.Run(`Given a publicIPProvider that returns error,
	When NewConn called,
	Then error returned`, func(t *testing.T) {
		e := fmt.Errorf("error from provider")
		conn, err := NewConn(context.Background(), WithPublicIPProvider(func() (string, error) {
			return "", e
		}))
		require.Equal(t, e, err)
		require.Nil(t, conn)
	})

}

func TestConnectionManager_Closing(t *testing.T) {
	t.Run(`Given a connection got from NewConn,
	When Close is called,
	Then the connection is closed and all the resources are freed`, func(t *testing.T) {
		initial := runtime.NumGoroutine()
		conn, err := NewConn(context.Background(), WithLoggingLevel(zerolog.TraceLevel), WithPublicIPProvider(func() (string, error) {
			return "123.123.123.123", nil
		}))
		require.NoError(t, err)

		<-time.After(100 * time.Millisecond)
		conn.Close()

		// wait for conn to close
		select {
		case e := <-conn.Errors():
			require.ErrorIsf(t, e, ErrNoConnectionEstablished,
				"expected conn error to be either ErrNoConnectionEstablished but it's %s", e)
		case <-time.After(time.Second):
			require.Fail(t, "expected for conn to receive ErrNoConnectionEstablished but received nothing")
		}
		runtime.GC() // we want to be sure that the library does not leak goroutines so instead of using time.After which also adds unreliability,
		// running GC is just faster and yields the information that we are interested into
		require.Equalf(t, initial, runtime.NumGoroutine(), "expected equal number of goroutines")
	})

	t.Run(`Given a connection got from NewConn,
	When the given context is cancelled,
	Then the connection is closed and all the resources are freed`, func(t *testing.T) {
		initial := runtime.NumGoroutine()
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := NewConn(ctx, WithLoggingLevel(zerolog.TraceLevel), WithPublicIPProvider(func() (string, error) {
			return "123.123.123.123", nil
		}))
		require.NoError(t, err)
		<-time.After(100 * time.Millisecond)
		cancel()

		// wait for conn to close
	errorsCheck:
		for {
			select {
			case e, ok := <-conn.Errors():
				if !ok {
					break errorsCheck
				}
				require.ErrorIsf(t, e, ErrNoConnectionEstablished,
					"expected conn error to be either ErrNoConnectionEstablished but it's %s", e)
			case <-time.After(time.Second):
				require.Fail(t, "expected for conn to receive ErrNoConnectionEstablished but received nothing")
			}
		}

		runtime.GC() // we want to be sure that the library does not leak goroutines so instead of using time.After which also adds unreliability,
		// running GC is just faster and yields the information that we are interested into
		require.Equalf(t, initial, runtime.NumGoroutine(), "expected equal number of goroutines")
	})
}

func TestConnectionManager_AttemptConnection(t *testing.T) {
	t.Run(`Given two connections,
	When one is connecting to another in the same time,
	Then both are using the same connection and can send and receive data`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		conn1, err := NewConn(ctx,
			WithLoggingTraceID("conn1"),
			WithLoggingLevel(zerolog.TraceLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(500*time.Millisecond),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)
		conn2, err := NewConn(ctx,
			WithLoggingTraceID("conn2"),
			WithLoggingLevel(zerolog.TraceLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(500*time.Millisecond),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)

		// channel to process data received by both connections
		receivedData := make(chan string, 3)
		defer close(receivedData)
		conn1.RegisterDataHandler(func(dat []byte) {
			receivedData <- fmt.Sprintf("conn1 received %s", string(dat))
		})
		conn2.RegisterDataHandler(func(dat []byte) {
			receivedData <- fmt.Sprintf("conn2 received %s", string(dat))
		})

		// Cancel everything after 2 seconds
		go func() {
			<-time.After(2 * time.Second)
			cancelFunc()
		}()
		var wg sync.WaitGroup
		// start first connection and listen for an established connection
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, conn1.AttemptConnection(conn2.LocalAddress(), conn2.PublicAddress()))
			<-conn1.EstablishedEvents()
			t.Log("conn1 connected")
		}()
		// start second connection and listen for an established connection
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, conn2.AttemptConnection(conn1.PublicAddress(), conn1.LocalAddress()))
			<-conn2.EstablishedEvents()
			t.Log("conn2 connected")
		}()
		wg.Wait()

		// write some data on the first connection
		require.NoError(t, conn1.Write([]byte("test from conn1")))
		require.NoError(t, conn2.Write([]byte("test from conn2")))

		// read the messages received and be sure that there isn't sent more than one info / connection side
		var msgCnt int
		expectedMsgs := 2
	readMessages:
		for i := 0; i < expectedMsgs; i++ {
			timer := time.NewTimer(1 * time.Second)
			select {
			case m := <-receivedData:
				msgCnt++
				if strings.HasPrefix(m, "conn1") {
					require.Equal(t, "conn1 received test from conn2", m)
					continue
				}
				require.Equal(t, "conn2 received test from conn1", m)
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				break readMessages
			}
		}
		require.Equal(t, 2, msgCnt)

		conn1.Close()
		conn2.Close()
		var cnt int
		for {
			e, ok := <-conn1.Errors()
			if ok {
				require.True(t,
					errors.Is(e, net.ErrClosed) || errors.Is(e, io.EOF),
					"expected conn1 error to be either net.ErrClosed or io.EOF but it's %s", e)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn1")

		cnt = 0
		for {
			e, ok := <-conn2.Errors()
			if ok {
				require.True(t,
					errors.Is(e, net.ErrClosed) || errors.Is(e, io.EOF),
					"expected conn2 error to be either net.ErrClosed or io.EOF but it's %s", e)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn2")
	})

	t.Run(`Given two connections connected one to another,
	When closed,
	Then resources are freed and channels closed`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		conn1, err := NewConn(ctx,
			WithLoggingTraceID("conn1"),
			WithLoggingLevel(zerolog.TraceLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(500*time.Millisecond),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)
		conn2, err := NewConn(ctx,
			WithLoggingTraceID("conn2"),
			WithLoggingLevel(zerolog.TraceLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(500*time.Millisecond),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)

		// Cancel everything after a timeout in case something blocks
		go func() {
			<-time.After(5 * time.Second)
			cancelFunc()
		}()
		var wg sync.WaitGroup
		// start first connection and listen for an established connection
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, conn1.AttemptConnection(conn2.LocalAddress(), conn2.PublicAddress()))
			<-conn1.EstablishedEvents()
			t.Log("conn1 connected")
		}()
		// start second connection and listen for an established connection
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, conn2.AttemptConnection(conn1.PublicAddress(), conn1.LocalAddress()))
			<-conn2.EstablishedEvents()
			t.Log("conn2 connected")
		}()
		wg.Wait()

		<-time.After(100 * time.Millisecond)
		conn1.Close()
		conn2.Close()

		// wait for conn1 to close
		var cnt int
		for {
			e, ok := <-conn1.Errors()
			if ok {
				require.True(t,
					errors.Is(e, net.ErrClosed) || errors.Is(e, io.EOF),
					"expected conn1 error to be either net.ErrClosed or io.EOF but it's %s", e)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn1")

		cnt = 0
		for {
			e, ok := <-conn2.Errors()
			if ok {
				require.True(t,
					errors.Is(e, net.ErrClosed) || errors.Is(e, io.EOF),
					"expected conn2 error to be either net.ErrClosed or io.EOF but it's %s", e)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn2")

		// When the connections are closed then Write will return ErrStopped
		require.Equal(t, ErrStopped, conn1.Write([]byte("test")))
		require.Equal(t, ErrStopped, conn2.Write([]byte("test")))
	})

	t.Run(`Given one connection,
	When is calling a non existing connection,
	Then error is returned from the Errors channel`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		conn1, err := NewConn(ctx,
			WithLoggingTraceID("conn1"),
			WithLoggingLevel(zerolog.WarnLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)

		// start connection and attempt to connect
		go func() {
			require.NoError(t, conn1.AttemptConnection("54.81.84.38:38283")) // non-existing address
			select {
			case <-conn1.EstablishedEvents():
				require.Fail(t, "unexpected established event")
			case <-time.After(500 * time.Millisecond):
			}
		}()

		// Cancel everything after 2 seconds
		go func() {
			<-time.After(2 * time.Second)
			cancelFunc()
		}()

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case err := <-conn1.Errors():
			if !timer.Stop() {
				<-timer.C
			}
			require.Equal(t, ErrNoConnectionEstablished, err)
		case <-timer.C:
			require.Fail(t, "no errors returned from the channel")
		}
	})

	t.Run(`Given one connection,
	When is calling AttemptConnection twice with a wrong IP,
	Then receives error that the connection is stopped and also the error channel is closed too`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		conn1, err := NewConn(ctx,
			WithLoggingTraceID("conn1"),
			WithLoggingLevel(zerolog.WarnLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)

		// start connection and attempt to connect
		go func() {
			require.NoError(t, conn1.AttemptConnection("54.81.84.38:38283")) // non-existing address
			select {
			case <-conn1.EstablishedEvents():
				require.Fail(t, "unexpected established event")
			case <-time.After(500 * time.Millisecond):
			}
		}()

		// Cancel everything after 2 seconds
		go func() {
			<-time.After(2 * time.Second)
			cancelFunc()
		}()

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case err := <-conn1.Errors():
			if !timer.Stop() {
				<-timer.C
			}
			require.Equal(t, ErrNoConnectionEstablished, err)
		case <-timer.C:
			require.Fail(t, "no errors returned from the channel")
		}
		require.Error(t, ErrAlreadyEstablished, conn1.AttemptConnection("54.81.84.38:38283")) // non-existing address

		timer = time.NewTimer(500 * time.Millisecond)
		select {
		case err, ok := <-conn1.Errors():
			if !timer.Stop() {
				<-timer.C
			}
			require.False(t, ok)
			require.Nil(t, err)
		case <-timer.C:
			require.Fail(t, "expected channel to be closed")
		}
	})

	t.Run(`Given one connection,
	When is calling AttemptConnection with more addresses than the number of NoOfAddressesToAttempt,
	Then receives error with the addresses not managed to schedule`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		conn, err := NewConn(ctx,
			WithLoggingTraceID("conn"),
			WithLoggingLevel(zerolog.WarnLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithNoOfAddressesToAttempt(2),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)

		// Cancel everything after 2 seconds
		go func() {
			<-time.After(2 * time.Second)
			cancelFunc()
		}()

		aErr := conn.AttemptConnection("54.81.84.38:38283", "51.81.84.38:38283", "55.81.84.38:38283") // non-existing addresses
		require.ErrorIs(t, aErr, ErrAddressesDismissed)
		require.Contains(t, aErr.Error(), "55.81.84.38:38283")
		select {
		case <-conn.EstablishedEvents():
			require.Fail(t, "unexpected established event")
		case <-time.After(500 * time.Millisecond):
		}
		conn.Close()
		<-time.After(200 * time.Millisecond)

		var cnt int
		for {
			e, ok := <-conn.Errors()
			if ok {
				require.ErrorIs(t, e, ErrNoConnectionEstablished)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn")
	})

	t.Run(`Given one connection,
	When is calling AttemptConnection with its own local address,
	The connection is established but it's closed right away because the connection object can hold only one net.Conn instance`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		conn, err := NewConn(ctx,
			WithLoggingTraceID("conn"),
			WithLoggingLevel(zerolog.TraceLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithNoOfAddressesToAttempt(2),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)

		// Cancel everything after 2 seconds
		go func() {
			<-time.After(2 * time.Second)
			cancelFunc()
		}()

		require.NoError(t, conn.AttemptConnection(conn.LocalAddress()))
		select {
		case <-conn.EstablishedEvents():
		case <-time.After(500 * time.Millisecond):
			require.Fail(t, "expected to have a connection")
		}
		//conn.Close()
		<-time.After(200 * time.Millisecond)

		var cnt int
		for {
			e, ok := <-conn.Errors()
			if ok {
				require.True(t,
					errors.Is(e, net.ErrClosed) || errors.Is(e, io.EOF),
					"expected conn1 error to be either net.ErrClosed or io.EOF but it's %s", e)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn")
	})

	t.Run(`Given three connections,
	When one connection is calling AttemptConnection with other two addresses,
	Then it connects to the first one and skips the second one`, func(t *testing.T) {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()

		// Given
		conn1, err := NewConn(ctx,
			WithLoggingTraceID("conn"),
			WithLoggingLevel(zerolog.WarnLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithNoOfAddressesToAttempt(2),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil
			}))
		require.NoError(t, err)
		conn2, err := NewConn(ctx,
			WithLoggingTraceID("conn"),
			WithLoggingLevel(zerolog.WarnLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithNoOfAddressesToAttempt(2),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.112", nil
			}))
		require.NoError(t, err)
		conn3, err := NewConn(ctx,
			WithLoggingTraceID("conn"),
			WithLoggingLevel(zerolog.WarnLevel),
			WithAttemptRetries(1),
			WithAttemptTimeout(100*time.Millisecond),
			WithNoOfAddressesToAttempt(2),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.113", nil
			}))
		require.NoError(t, err)

		receivedData := make(chan string, 3)
		conn1.RegisterDataHandler(func(dat []byte) {
			receivedData <- fmt.Sprintf("conn1 received %s", string(dat))
		})
		conn2.RegisterDataHandler(func(dat []byte) {
			receivedData <- fmt.Sprintf("conn2 received %s", string(dat))
		})
		conn3.RegisterDataHandler(func(dat []byte) {
			receivedData <- fmt.Sprintf("conn3 received %s", string(dat))
		})

		// Cancel everything after 2 seconds
		go func() {
			<-time.After(2 * time.Second)
			cancelFunc()
		}()

		// When
		require.NoError(t, conn1.AttemptConnection(conn2.LocalAddress(), conn3.LocalAddress()))
		select {
		case _, ok := <-conn1.EstablishedEvents():
			require.False(t, ok)
		case <-time.After(500 * time.Millisecond):
			require.Fail(t, "expected established conn from conn1")
		}
		select {
		case _, ok := <-conn2.EstablishedEvents():
			require.False(t, ok)
		case <-time.After(500 * time.Millisecond):
			require.Fail(t, "expected established conn from conn2")
		}
		select {
		case <-conn3.EstablishedEvents():
			require.Fail(t, "unexpected established event from conn3")
		case <-time.After(300 * time.Millisecond):
			conn3.Close()
		}

		require.NoError(t, conn1.Write([]byte("test from conn1")))
		require.NoError(t, conn2.Write([]byte("test from conn2")))

		// Then
		// read the messages received and be sure that there isn't sent more than one info / connection side
		var msgCnt int
		expectedMsgs := 2
	readMessages:
		for i := 0; i < expectedMsgs; i++ {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case m := <-receivedData:
				msgCnt++
				if strings.HasPrefix(m, "conn1") {
					require.Equal(t, "conn1 received test from conn2", m)
					continue
				}
				require.Equal(t, "conn2 received test from conn1", m)
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				break readMessages
			}
		}
		require.Equal(t, 2, msgCnt)
		// close conn1 and check conn2 too to see that it's closed
		conn1.Close()
		<-time.After(100 * time.Millisecond)

		var cnt int
		for {
			e, ok := <-conn1.Errors()
			if ok {
				require.ErrorIs(t, e, net.ErrClosed)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn1")
		cnt = 0
		for {
			e, ok := <-conn2.Errors()
			if ok {
				require.True(t,
					errors.Is(e, net.ErrClosed) || errors.Is(e, io.EOF),
					"expected conn2 error to be either net.ErrClosed or io.EOF but it's %s", e)
				cnt++
				continue
			}
			break
		}
		cnt = 0
		for {
			e, ok := <-conn3.Errors()
			if ok {
				require.ErrorIs(t, e, ErrNoConnectionEstablished)
				cnt++
				continue
			}
			break
		}
		require.GreaterOrEqualf(t, cnt, 1, "conn3")
	})
}

func TestConnectionManager_LocalAddress(t *testing.T) {
	t.Run(`Given a localIPProvider that returns ip,
	When LocalAddress is called,
	The IP from the provider is returned containing the port from the localListener`, func(t *testing.T) {
		localIP := "123.123.123.123"
		conn, err := NewConn(context.Background(),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil // just to avoid to call third-party during unit tests
			}),
			WithLocalIPProvider(func() (string, error) {
				return localIP, nil
			}),
		)
		require.Nil(t, err)
		require.NotNil(t, conn)
		address := conn.LocalAddress()
		require.Contains(t, address, localIP)
		require.True(t, strings.Count(address, ":") == 1)
	})

	t.Run(`Given a localIPProvider that returns error,
	When LocalAddress is called,
	The IP from the provider is returned containing the port from the localListener`, func(t *testing.T) {
		conn, err := NewConn(context.Background(),
			WithPublicIPProvider(func() (string, error) {
				return "111.111.111.111", nil // just to avoid to call third-party during unit tests
			}),
			WithLocalIPProvider(func() (string, error) {
				return "", fmt.Errorf("error from local IP provider")
			}),
		)
		require.Nil(t, err)
		require.NotNil(t, conn)
		address := conn.LocalAddress()
		require.Contains(t, address, "[")
		require.Contains(t, address, "]")
		require.True(t, strings.Count(address, ":") == 3)
	})
}
