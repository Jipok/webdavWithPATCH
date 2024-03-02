## webdavWithPATCH
Adds the ability to the [standard go WebDav server](https://pkg.go.dev/golang.org/x/net/webdav) to update files partially.

Made through implementation of [SaberDAV HTTP PATCH](https://sabre.io/dav/http-patch/)

Useful for use in clients that mount webdav as a file system (for example [webdavfs](https://github.com/miquels/webdavfs)).

Example usage:
```go
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/Jipok/webdavWithPATCH"
	"golang.org/x/net/webdav"
)

var (
	Addr      string
	TargetDir string
)

func main() {
	flag.StringVar(&Addr, "l", "127.0.0.1:8080", "interface:port for WebDav server to listen")
	flag.StringVar(&TargetDir, "t", "./", "target dir")
	flag.Parse()

	fs := webdav.Dir(TargetDir)
	handler := &webdavWithPATCH.Handler{
		Handler: webdav.Handler{
			FileSystem: fs,
			LockSystem: webdav.NewMemLS(),
		},
	}

	server := &http.Server{
		Addr:    Addr,
		Handler: handler,
	}

	log.Printf("Starting WebDAV server on http://%s \n", Addr)

	err := server.ListenAndServe()
	if err != nil {
		log.Fatal("Error starting server: ", err)
	}
}

```