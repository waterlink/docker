package docker

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mkTestContext generates a build context from the contents of the provided dockerfile.
// This context is suitable for use as an argument to BuildFile.Build()
func mkTestContext(dockerfile string, files [][2]string, t *testing.T) Archive {
	context, err := mkBuildContext(dockerfile, files)
	if err != nil {
		t.Fatal(err)
	}
	return context
}

// A testContextTemplate describes a build context and how to test it
type testContextTemplate struct {
	// Contents of the Dockerfile
	dockerfile string
	// Additional files in the context, eg [][2]string{"./passwd", "gordon"}
	files [][2]string
	// Additional remote files to host on a local HTTP server.
	remoteFiles [][2]string
}

// A table of all the contexts to build and test.
// A new docker runtime will be created and torn down for each context.
var testContexts = []testContextTemplate{
	{
		`
from   {IMAGE}
run    sh -c 'echo root:testpass > /tmp/passwd'
run    mkdir -p /var/run/sshd
run    [ "$(cat /tmp/passwd)" = "root:testpass" ]
run    [ "$(ls -d /var/run/sshd)" = "/var/run/sshd" ]
`,
		nil,
		nil,
	},

	{
		`
from {IMAGE}
add foo /usr/lib/bla/bar
run [ "$(cat /usr/lib/bla/bar)" = 'hello' ]
add http://{SERVERADDR}/baz /usr/lib/baz/quux
run [ "$(cat /usr/lib/baz/quux)" = 'world!' ]
`,
		[][2]string{{"foo", "hello"}},
		[][2]string{{"/baz", "world!"}},
	},

	{
		`
from {IMAGE}
add f /
run [ "$(cat /f)" = "hello" ]
add f /abc
run [ "$(cat /abc)" = "hello" ]
add f /x/y/z
run [ "$(cat /x/y/z)" = "hello" ]
add f /x/y/d/
run [ "$(cat /x/y/d/f)" = "hello" ]
add d /
run [ "$(cat /ga)" = "bu" ]
add d /somewhere
run [ "$(cat /somewhere/ga)" = "bu" ]
add d /anotherplace/
run [ "$(cat /anotherplace/ga)" = "bu" ]
add d /somewheeeere/over/the/rainbooow
run [ "$(cat /somewheeeere/over/the/rainbooow/ga)" = "bu" ]
`,
		[][2]string{
			{"f", "hello"},
			{"d/ga", "bu"},
		},
		nil,
	},

	{
		`
from {IMAGE}
add http://{SERVERADDR}/x /a/b/c
run [ "$(cat /a/b/c)" = "hello" ]
add http://{SERVERADDR}/x?foo=bar /
run [ "$(cat /x)" = "hello" ]
add http://{SERVERADDR}/x /d/
run [ "$(cat /d/x)" = "hello" ]
add http://{SERVERADDR} /e
run [ "$(cat /e)" = "blah" ]
`,
		nil,
		[][2]string{{"/x", "hello"}, {"/", "blah"}},
	},

	{
		`
from   {IMAGE}
env    FOO BAR
run    [ "$FOO" = "BAR" ]
`,
		nil,
		nil,
	},

	{
		`
from {IMAGE}
ENTRYPOINT /bin/echo
CMD Hello world
`,
		nil,
		nil,
	},

	{
		`
from {IMAGE}
VOLUME /test
CMD Hello world
`,
		nil,
		nil,
	},

	{
		`
from {IMAGE}
env    FOO /foo/baz
env    BAR /bar
env    BAZ $BAR
env    FOOPATH $PATH:$FOO
run    [ "$BAR" = "$BAZ" ]
run    [ "$FOOPATH" = "$PATH:/foo/baz" ]
`,
		nil,
		nil,
	},

	{
		`
from {IMAGE}
env    FOO /bar
env    TEST testdir
env    BAZ /foobar
add    testfile $BAZ/
add    $TEST $FOO
run    [ "$(cat /foobar/testfile)" = "test1" ]
run    [ "$(cat /bar/withfile)" = "test2" ]
`,
		[][2]string{
			{"testfile", "test1"},
			{"testdir/withfile", "test2"},
		},
		nil,
	},
}

// FIXME: test building with 2 successive overlapping ADD commands

func constructDockerfile(template string, ip net.IP, port string) string {
	serverAddr := fmt.Sprintf("%s:%s", ip, port)
	replacer := strings.NewReplacer("{IMAGE}", unitTestImageID, "{SERVERADDR}", serverAddr)
	return replacer.Replace(template)
}

func mkTestingFileServer(files [][2]string) (*httptest.Server, error) {
	mux := http.NewServeMux()
	for _, file := range files {
		name, contents := file[0], file[1]
		mux.HandleFunc(name, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(contents))
		})
	}

	// This is how httptest.NewServer sets up a net.Listener, except that our listener must accept remote
	// connections (from the container).
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}

	s := httptest.NewUnstartedServer(mux)
	s.Listener = listener
	s.Start()
	return s, nil
}

func TestBuild(t *testing.T) {
	for _, ctx := range testContexts {
		buildImage(ctx, t, nil, true)
	}
}

func buildImage(context testContextTemplate, t *testing.T, srv *Server, useCache bool) *Image {
	if srv == nil {
		runtime, err := newTestRuntime()
		if err != nil {
			t.Fatal(err)
		}
		defer nuke(runtime)

		srv = &Server{
			runtime:     runtime,
			pullingPool: make(map[string]struct{}),
			pushingPool: make(map[string]struct{}),
		}
	}

	httpServer, err := mkTestingFileServer(context.remoteFiles)
	if err != nil {
		t.Fatal(err)
	}
	defer httpServer.Close()

	idx := strings.LastIndex(httpServer.URL, ":")
	if idx < 0 {
		t.Fatalf("could not get port from test http server address %s", httpServer.URL)
	}
	port := httpServer.URL[idx+1:]

	ip := srv.runtime.networkManager.bridgeNetwork.IP
	dockerfile := constructDockerfile(context.dockerfile, ip, port)

	buildfile := NewBuildFile(srv, ioutil.Discard, false, useCache)
	id, err := buildfile.Build(mkTestContext(dockerfile, context.files, t))
	if err != nil {
		t.Fatal(err)
	}

	img, err := srv.ImageInspect(id)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestVolume(t *testing.T) {
	img := buildImage(testContextTemplate{`
        from {IMAGE}
        volume /test
        cmd Hello world
    `, nil, nil}, t, nil, true)

	if len(img.Config.Volumes) == 0 {
		t.Fail()
	}
	for key := range img.Config.Volumes {
		if key != "/test" {
			t.Fail()
		}
	}
}

func TestBuildMaintainer(t *testing.T) {
	img := buildImage(testContextTemplate{`
        from {IMAGE}
        maintainer dockerio
    `, nil, nil}, t, nil, true)

	if img.Author != "dockerio" {
		t.Fail()
	}
}

func TestBuildEnv(t *testing.T) {
	img := buildImage(testContextTemplate{`
        from {IMAGE}
        env port 4243
        `,
		nil, nil}, t, nil, true)
	hasEnv := false
	for _, envVar := range img.Config.Env {
		if envVar == "port=4243" {
			hasEnv = true
			break
		}
	}
	if !hasEnv {
		t.Fail()
	}
}

func TestBuildCmd(t *testing.T) {
	img := buildImage(testContextTemplate{`
        from {IMAGE}
        cmd ["/bin/echo", "Hello World"]
        `,
		nil, nil}, t, nil, true)

	if img.Config.Cmd[0] != "/bin/echo" {
		t.Log(img.Config.Cmd[0])
		t.Fail()
	}
	if img.Config.Cmd[1] != "Hello World" {
		t.Log(img.Config.Cmd[1])
		t.Fail()
	}
}

func TestBuildExpose(t *testing.T) {
	img := buildImage(testContextTemplate{`
        from {IMAGE}
        expose 4243
        `,
		nil, nil}, t, nil, true)

	if img.Config.PortSpecs[0] != "4243" {
		t.Fail()
	}
}

func TestBuildEntrypoint(t *testing.T) {
	img := buildImage(testContextTemplate{`
        from {IMAGE}
        entrypoint ["/bin/echo"]
        `,
		nil, nil}, t, nil, true)

	if img.Config.Entrypoint[0] != "/bin/echo" {
	}
}

// testing #1405 - config.Cmd does not get cleaned up if
// utilizing cache
func TestBuildEntrypointRunCleanup(t *testing.T) {
	runtime, err := newTestRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer nuke(runtime)

	srv := &Server{
		runtime:     runtime,
		pullingPool: make(map[string]struct{}),
		pushingPool: make(map[string]struct{}),
	}

	img := buildImage(testContextTemplate{`
        from {IMAGE}
        run echo "hello"
        `,
		nil, nil}, t, srv, true)

	img = buildImage(testContextTemplate{`
        from {IMAGE}
        run echo "hello"
        add foo /foo
        entrypoint ["/bin/echo"]
        `,
		[][2]string{{"foo", "HEYO"}}, nil}, t, srv, true)

	if len(img.Config.Cmd) != 0 {
		t.Fail()
	}
}

func TestBuildImageWithCache(t *testing.T) {
	runtime, err := newTestRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer nuke(runtime)

	srv := &Server{
		runtime:     runtime,
		pullingPool: make(map[string]struct{}),
		pushingPool: make(map[string]struct{}),
	}

	template := testContextTemplate{`
        from {IMAGE}
        maintainer dockerio
        `,
		nil, nil}

	img := buildImage(template, t, srv, true)
	imageId := img.ID

	img = nil
	img = buildImage(template, t, srv, true)

	if imageId != img.ID {
		t.Logf("Image ids should match: %s != %s", imageId, img.ID)
		t.Fail()
	}
}

func TestBuildImageWithoutCache(t *testing.T) {
	runtime, err := newTestRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer nuke(runtime)

	srv := &Server{
		runtime:     runtime,
		pullingPool: make(map[string]struct{}),
		pushingPool: make(map[string]struct{}),
	}

	template := testContextTemplate{`
        from {IMAGE}
        maintainer dockerio
        `,
		nil, nil}

	img := buildImage(template, t, srv, true)
	imageId := img.ID

	img = nil
	img = buildImage(template, t, srv, false)

	if imageId == img.ID {
		t.Logf("Image ids should not match: %s == %s", imageId, img.ID)
		t.Fail()
	}
}

func TestForbiddenContextPath(t *testing.T) {
	runtime, err := newTestRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer nuke(runtime)

	srv := &Server{
		runtime:     runtime,
		pullingPool: make(map[string]struct{}),
		pushingPool: make(map[string]struct{}),
	}

	context := testContextTemplate{`
        from {IMAGE}
        maintainer dockerio
        add ../../ test/
        `,
		[][2]string{{"test.txt", "test1"}, {"other.txt", "other"}}, nil}

	httpServer, err := mkTestingFileServer(context.remoteFiles)
	if err != nil {
		t.Fatal(err)
	}
	defer httpServer.Close()

	idx := strings.LastIndex(httpServer.URL, ":")
	if idx < 0 {
		t.Fatalf("could not get port from test http server address %s", httpServer.URL)
	}
	port := httpServer.URL[idx+1:]

	ip := srv.runtime.networkManager.bridgeNetwork.IP
	dockerfile := constructDockerfile(context.dockerfile, ip, port)

	buildfile := NewBuildFile(srv, ioutil.Discard, false, true)
	_, err = buildfile.Build(mkTestContext(dockerfile, context.files, t))

	if err == nil {
		t.Log("Error should not be nil")
		t.Fail()
	}

	if err.Error() != "Forbidden path: /" {
		t.Logf("Error message is not expected: %s", err.Error())
		t.Fail()
	}
}
