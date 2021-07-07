package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const AutosaveOffset = 7
const AutosaveTime = 5 * time.Second

const Website = `
<!DOCTYPE html>
<html>

<head>
  <title>Mindustry Manager</title>
  <meta charset="utf-8">
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/bulma@0.9.3/css/bulma.min.css">
</head>

<body>
  <section class="section">
    <div class="container">
      <form class="box" action="/" method="POST">
        <div class="field is-grouped">
          <div class="control"><input class="input" type="text" name="map" value="Molten_Lake"></div>
          <div class="control"><input class="button is-success" type="submit" name="act" value="start"></div>
          <div class="control"><input class="button is-danger" type="submit" name="act" value="stop"></div>
        </div>
<hr>
        <div class="field is-grouped">
          <div class="control">
            <div class="select">
              <select name="save">
                {{ range $nr := .Saves }}
                <option value="{{ $nr }}">{{ $nr }}</option>
                {{ end }}
              </select>
            </div>
          </div>
          <div class="control"><input class="button is-success" type="submit" name="act" value="load"></div>
        </div>
        <div class="field is-grouped">
          <div class="control"><input class="input" type="text" name="fsname" value="mysafe"></div>
          <div class="control"><input class="button is-success" type="submit" name="act" value="fssave"></div>
          <div class="control">
            <div class="select">
              <select name="fssave">
                {{ range $name := .FSSaves }}
                <option value="{{ $name }}">{{ $name }}</option>
                {{ end }}
              </select>
            </div>
          </div>
          <div class="control"><input class="button is-success" type="submit" name="act" value="fsload"></div>
        </div>
        <div class="field is-grouped">
          <div class="control"><input class="button" type="submit" name="act" value="pauseon"></div>
          <div class="control"><input class="button" type="submit" name="act" value="pauseoff"></div>
        </div>
      </form>
    </div>
  </section>
</body>

</html>
`

func main() {
	// run server until it stops
	server, errc := NewStartedServer()

	err := server.StartGame("Molten_Lake")
	if err != nil {
		log.Fatalf("unable to start game: %s", err)
	}

	tmpl, err := template.New("site").Parse(Website)
	if err != nil {
		log.Fatalf("unable to render template: %s", err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			var err error
			switch r.Form["act"][0] {
			case "start":
				err = server.StartGame(r.Form["map"][0])
			case "stop":
				err = server.Stop()
			case "pauseon":
				err = server.Pause(true)
			case "pauseoff":
				err = server.Pause(false)
			case "load":
				if len(r.Form["save"]) == 1 {
					err = server.Load(r.Form["save"][0])
				}
			case "fssave":
				if len(r.Form["fsname"]) == 1 {
					err = server.Save(r.Form["fsname"][0])
				}
			case "fsload":
				if len(r.Form["fssave"]) == 1 {
					err = server.Save(r.Form["fssave"][0])
				}
			}
			if err != nil {
				log.Printf("unable to notify mindustry: %s", err)
			}
		}
		w.Header().Add("Content-Type", "text/html")
		_ = tmpl.Execute(w, server)
	})
	go http.ListenAndServe(":8080", nil)

	// handle os signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		server.Exit()
	}()

	// handle server crash
	err = <-errc
	if err != nil {
		log.Fatalf("unable to run binary: %s", err)
	}
	_ = server
}

type (
	Server struct {
		cmd     *exec.Cmd
		stdin   io.Writer
		stdout  io.ReadCloser
		started bool

		saveTicker *time.Ticker
		saveEnable bool
		saveN      int
	}
)

// NewStartedServer starts a mindustry server
func NewStartedServer() (*Server, <-chan error) {
	cmd := exec.Command("java", "-jar", "server-release.jar")
	stdinPipe, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// also redirect stdin to stdout to show commands
	stdin := io.MultiWriter(stdinPipe, os.Stdout)

	s := &Server{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		started: false,

		saveEnable: true,
		saveTicker: time.NewTicker(AutosaveTime),
	}
	errc := make(chan error, 1)
	go func() {
		errc <- cmd.Run()
	}()

	// log to stdout
	go func() {
		io.Copy(os.Stdout, stdout)
	}()

	<-time.After(1 * time.Second)
	go s.AutosaveHandler()
	return s, errc
}

func (s *Server) Pause(on bool) error {
	s.saveEnable = !on
	value := "off"
	if on {
		value = "on"
	}
	_, err := fmt.Fprintf(s.stdin, "pause %s\n", value)
	if err != nil {
		return fmt.Errorf("unable to pause game: %w", err)
	}
	return nil
}

func (s *Server) StartGame(mapname string) error {
	s.saveN = 0
	s.ResetAutosaveTimer()
	err := s.Stop()
	_, err = fmt.Fprintf(s.stdin, "host %s survival\n", mapname)
	if err != nil {
		return fmt.Errorf("unable to start game: %w", err)
	}
	s.started = true
	return nil
}

func (s *Server) Stop() error {
	s.started = false
	_, err := fmt.Fprint(s.stdin, "stop\n")
	if err != nil {
		return fmt.Errorf("unable to stop previous game: %w", err)
	}
	return nil
}

func (s *Server) Exit() error {
	err := s.Stop()
	_, err = fmt.Fprintf(s.stdin, "exit\n")
	if err != nil {
		return fmt.Errorf("unable to exit game: %w", err)
	}
	return nil
}

func (s *Server) Terminate() error {
	return s.cmd.Process.Kill()
}

func (s *Server) ResetAutosaveTimer() {
	s.saveTicker.Reset(AutosaveTime)
}

func (s *Server) FSSaves() []string {
	saves := []string{}
	all, _ := filepath.Glob("config/saves/*.msav")
	for _, save := range all {
		if !strings.Contains(save, "autosave") {
			save = strings.TrimPrefix(save, "config/saves/")
			save = strings.TrimSuffix(save, ".msav")
			saves = append(saves, save)
		}
	}
	return saves
}

func (s *Server) AutosaveHandler() {
	for range s.saveTicker.C {
		if !s.started {
			continue
		}
		if !s.saveEnable {
			continue
		}
		err := s.Save(fmt.Sprintf("autosave-%d", s.saveN))
		if err != nil {
			log.Printf("unable to save game: %s", err)
		}
		s.saveN++
	}
}

func (s *Server) Saves() []string {
	saves := make([]string, s.saveN)
	for i := 0; i < s.saveN; i++ {
		saves[s.saveN-1-i] = fmt.Sprintf("autosave-%d", i)
	}
	return saves
}

func (s *Server) Save(name string) error {
	_, err := fmt.Fprintf(s.stdin, "save %s\n", name)
	return err
}

func (s *Server) Load(name string) error {
	s.ResetAutosaveTimer()
	err := s.Stop()
	if err != nil {
		return fmt.Errorf("unable to stop game: %w", err)
	}
	_, err = fmt.Fprintf(s.stdin, "load %s\n", name)
	if err != nil {
		return fmt.Errorf("unable to load game: %w", err)
	}
	s.started = true
	return nil
}
