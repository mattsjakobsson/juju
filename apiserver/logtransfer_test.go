// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package apiserver_test

import (
	"bufio"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/juju/loggo"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils"
	"golang.org/x/net/websocket"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/names.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/permission"
	"github.com/juju/juju/state"
	coretesting "github.com/juju/juju/testing"
	"github.com/juju/juju/testing/factory"
	"github.com/juju/juju/version"
)

type logtransferSuite struct {
	authHTTPSuite
	userTag         names.UserTag
	password        string
	machineTag      names.MachineTag
	machinePassword string
	logs            loggo.TestWriter
}

var _ = gc.Suite(&logtransferSuite{})

func (s *logtransferSuite) SetUpTest(c *gc.C) {
	s.authHTTPSuite.SetUpTest(c)
	s.password = "jabberwocky"
	u := s.Factory.MakeUser(c, &factory.UserParams{Password: s.password})
	s.userTag = u.Tag().(names.UserTag)
	m, password := s.Factory.MakeMachineReturningPassword(c, &factory.MachineParams{
		Nonce: "nonce",
	})
	s.machineTag = m.Tag().(names.MachineTag)
	s.machinePassword = password

	s.setUserAccess(c, permission.SuperuserAccess)
	s.setMigrationMode(c, state.MigrationModeImporting)

	s.logs.Clear()
	writer := loggo.NewMinimumLevelWriter(&s.logs, loggo.INFO)
	c.Assert(loggo.RegisterWriter("logsink-tests", writer), jc.ErrorIsNil)
}

func (s *logtransferSuite) logtransferURL(c *gc.C, scheme string) *url.URL {
	server := s.makeURL(c, scheme, "/migrate/logtransfer", nil)
	query := server.Query()
	query.Set("jujuclientversion", version.Current.String())
	server.RawQuery = query.Encode()
	return server
}

func (s *logtransferSuite) makeAuthHeader() http.Header {
	header := utils.BasicAuthHeader(s.userTag.String(), s.password)
	header.Add(params.MigrationModelHeader, s.State.ModelUUID())
	return header
}

func (s *logtransferSuite) dialWebsocket(c *gc.C) *websocket.Conn {
	return s.dialWebsocketInternal(c, s.makeAuthHeader())
}

func (s *logtransferSuite) dialWebsocketInternal(c *gc.C, header http.Header) *websocket.Conn {
	server := s.logtransferURL(c, "wss").String()
	return dialWebsocketFromURL(c, server, header)
}

func (s *logtransferSuite) TestRejectsMissingModelHeader(c *gc.C) {
	header := utils.BasicAuthHeader(s.userTag.String(), s.password)
	reader := s.toReader(s.dialWebsocketInternal(c, header))
	assertJSONError(c, reader, `unknown model: ""`)
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestRejectsBadMigratingModelUUID(c *gc.C) {
	header := utils.BasicAuthHeader(s.userTag.String(), s.password)
	header.Add(params.MigrationModelHeader, "does-not-exist")
	reader := s.toReader(s.dialWebsocketInternal(c, header))
	assertJSONError(c, reader, `unknown model: "does-not-exist"`)
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestRejectsInvalidVersion(c *gc.C) {
	url := s.logtransferURL(c, "wss")
	query := url.Query()
	query.Set("jujuclientversion", "blah")
	url.RawQuery = query.Encode()
	conn := dialWebsocketFromURL(c, url.String(), s.makeAuthHeader())
	defer conn.Close()
	reader := bufio.NewReader(conn)
	assertJSONError(c, reader, `^invalid jujuclientversion "blah".*`)
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestRejectsMachineLogins(c *gc.C) {
	header := utils.BasicAuthHeader(s.machineTag.String(), s.machinePassword)
	header.Add(params.MachineNonceHeader, "nonce")
	reader := s.toReader(s.dialWebsocketInternal(c, header))
	assertJSONError(c, reader, `tag kind machine not valid`)
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestRejectsBadPasword(c *gc.C) {
	header := utils.BasicAuthHeader(s.userTag.String(), "wrong")
	header.Add(params.MigrationModelHeader, s.State.ModelUUID())
	reader := s.toReader(s.dialWebsocketInternal(c, header))
	assertJSONError(c, reader, "invalid entity name or password")
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestRequiresSuperUser(c *gc.C) {
	s.setUserAccess(c, permission.AddModelAccess)
	reader := s.toReader(s.dialWebsocketInternal(c, s.makeAuthHeader()))
	assertJSONError(c, reader, `not a controller admin`)
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestRequiresMigratingModel(c *gc.C) {
	s.setMigrationMode(c, state.MigrationModeNone)
	reader := s.toReader(s.dialWebsocket(c))
	assertJSONError(c, reader, `model not importing`)
	assertWebsocketClosed(c, reader)
}

func (s *logtransferSuite) TestLogging(c *gc.C) {
	conn := s.dialWebsocket(c)
	reader := s.toReader(conn)

	// Read back the nil error, indicating that all is well.
	errResult := readJSONErrorLine(c, reader)
	c.Assert(errResult.Error, gc.IsNil)

	t0 := time.Date(2015, time.June, 1, 23, 2, 1, 0, time.UTC)
	err := websocket.JSON.Send(conn, &params.LogRecord{
		Entity:   "machine-23",
		Time:     t0,
		Module:   "some.where",
		Location: "foo.go:42",
		Level:    loggo.INFO.String(),
		Message:  "all is well",
	})
	c.Assert(err, jc.ErrorIsNil)

	t1 := time.Date(2015, time.June, 1, 23, 2, 2, 0, time.UTC)
	err = websocket.JSON.Send(conn, &params.LogRecord{
		Entity:   "machine-101",
		Time:     t1,
		Module:   "else.where",
		Location: "bar.go:99",
		Level:    loggo.ERROR.String(),
		Message:  "oh noes",
	})
	c.Assert(err, jc.ErrorIsNil)

	// Wait for the log documents to be written to the DB.
	logsColl := s.State.MongoSession().DB("logs").C("logs")
	var docs []bson.M
	for a := coretesting.LongAttempt.Start(); a.Next(); {
		err := logsColl.Find(nil).Sort("t").All(&docs)
		c.Assert(err, jc.ErrorIsNil)
		if len(docs) == 2 {
			break
		}
		if len(docs) >= 2 {
			c.Fatalf("saw more log documents than expected")
		}
		if !a.HasNext() {
			c.Fatalf("timed out waiting for log writes")
		}
	}

	// Check the recorded logs are correct.
	modelUUID := s.State.ModelUUID()
	c.Assert(docs[0]["t"], gc.Equals, t0.UnixNano())
	c.Assert(docs[0]["e"], gc.Equals, modelUUID)
	c.Assert(docs[0]["n"], gc.Equals, "machine-23")
	c.Assert(docs[0]["m"], gc.Equals, "some.where")
	c.Assert(docs[0]["l"], gc.Equals, "foo.go:42")
	c.Assert(docs[0]["v"], gc.Equals, int(loggo.INFO))
	c.Assert(docs[0]["x"], gc.Equals, "all is well")

	c.Assert(docs[1]["t"], gc.Equals, t1.UnixNano())
	c.Assert(docs[1]["e"], gc.Equals, modelUUID)
	c.Assert(docs[1]["n"], gc.Equals, "machine-101")
	c.Assert(docs[1]["m"], gc.Equals, "else.where")
	c.Assert(docs[1]["l"], gc.Equals, "bar.go:99")
	c.Assert(docs[1]["v"], gc.Equals, int(loggo.ERROR))
	c.Assert(docs[1]["x"], gc.Equals, "oh noes")

	// Close connection.
	err = conn.Close()
	c.Assert(err, jc.ErrorIsNil)

	// Ensure that no error is logged when the connection is closed
	// normally.
	shortAttempt := &utils.AttemptStrategy{
		Total: coretesting.ShortWait,
		Delay: 2 * time.Millisecond,
	}
	for a := shortAttempt.Start(); a.Next(); {
		for _, log := range s.logs.Log() {
			c.Assert(log.Level, jc.LessThan, loggo.ERROR, gc.Commentf("log: %#v", log))
		}
	}

	// Check that the logtransfer file was populated as expected
	logPath := filepath.Join(s.LogDir, "migrated.log")
	logContents, err := ioutil.ReadFile(logPath)
	c.Assert(err, jc.ErrorIsNil)
	line0 := modelUUID + ": machine-23 2015-06-01 23:02:01 INFO some.where foo.go:42 all is well\n"
	line1 := modelUUID + ": machine-101 2015-06-01 23:02:02 ERROR else.where bar.go:99 oh noes\n"
	c.Assert(string(logContents), gc.Equals, line0+line1)

	// Check the file mode is as expected. This doesn't work on
	// Windows (but this code is very unlikely to run on Windows so
	// it's ok).
	if runtime.GOOS != "windows" {
		info, err := os.Stat(logPath)
		c.Assert(err, jc.ErrorIsNil)
		c.Assert(info.Mode(), gc.Equals, os.FileMode(0600))
	}
}

func (s *logtransferSuite) toReader(conn *websocket.Conn) *bufio.Reader {
	s.AddCleanup(func(_ *gc.C) { conn.Close() })
	return bufio.NewReader(conn)
}

func (s *logtransferSuite) setUserAccess(c *gc.C, level permission.Access) {
	controllerTag := names.NewControllerTag(s.ControllerConfig.ControllerUUID())
	_, err := s.State.SetUserAccess(s.userTag, controllerTag, level)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *logtransferSuite) setMigrationMode(c *gc.C, mode state.MigrationMode) {
	model, err := s.State.Model()
	c.Assert(err, jc.ErrorIsNil)
	err = model.SetMigrationMode(mode)
	c.Assert(err, jc.ErrorIsNil)
}
