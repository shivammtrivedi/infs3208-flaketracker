package httpx

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// Our Go images are "distroless": they contain the service binary and nothing
// else - no shell, no curl, no wget. That makes them small and hard to attack,
// but it also means Docker Compose can't health-check a container by running
// `curl localhost:8080/readyz` inside it, because there is no curl.
//
// So each service binary can check itself: `/app healthcheck` calls the
// service's own /readyz endpoint and exits 0 (ready) or 1 (not ready).
// Compose runs that as the container's HEALTHCHECK, which lets other
// containers wait with `depends_on: condition: service_healthy`.
// (Kubernetes doesn't need this: it probes /readyz over HTTP itself.)

// HealthcheckCommand is called first thing in main(). If the program was
// started as `app healthcheck` it performs the check and exits; otherwise it
// returns immediately and the service starts normally.
func HealthcheckCommand() {
	if len(os.Args) < 2 || os.Args[1] != "healthcheck" {
		return
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := Healthcheck("http://127.0.0.1:" + port); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Healthcheck GETs baseURL/readyz and returns nil only for a 200 response.
// It's separate from HealthcheckCommand so it can be unit tested (a function
// that calls os.Exit can't be).
func Healthcheck(baseURL string) error {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/readyz returned %d", resp.StatusCode)
	}
	return nil
}
