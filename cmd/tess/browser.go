package main

import (
	"os/exec"
	"runtime"
)

// openBrowser opens link in the default browser without waiting for it.
// Tests replace it so no real browser starts.
var openBrowser = func(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Not "cmd /c start": cmd treats & in the query string as a command separator.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	case "darwin":
		cmd = exec.Command("open", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap the launcher; it exits once the browser has the link
	return nil
}
