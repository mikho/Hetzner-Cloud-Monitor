package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var dockerSockets = []string{"/run/docker.sock", "/var/run/docker.sock"}

// Docker is reported on every snapshot. Installed is detected continuously,
// so the dashboard can offer the opt-in when Docker shows up after install.
type Docker struct {
	Installed  bool        `json:"installed"`
	Enabled    bool        `json:"enabled"`
	Error      string      `json:"error,omitempty"`
	Containers []Container `json:"containers"`
}

type Port struct {
	Private int    `json:"private"`
	Public  int    `json:"public,omitempty"`
	Type    string `json:"type"`
	IP      string `json:"ip,omitempty"`
}

// Container leaves out environment variables, commands, labels and mounts on
// purpose: they often hold secrets or internal paths the dashboard doesn't need.
type Container struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Image        string   `json:"image"`
	Project      string   `json:"project,omitempty"`
	State        string   `json:"state"`
	Status       string   `json:"status"`
	Health       string   `json:"health,omitempty"`
	ExitCode     int      `json:"exitCode"`
	RestartCount int      `json:"restartCount"`
	StartedAt    int64    `json:"startedAt"`
	CPU          *float64 `json:"cpu"`
	Memory       *uint64  `json:"memory"`
	MemoryLimit  *uint64  `json:"memoryLimit"`
	Ports        []Port   `json:"ports"`
}

func dockerSocket() string {
	for _, s := range dockerSockets {
		if st, err := os.Stat(s); err == nil && st.Mode()&os.ModeSocket != 0 {
			return s
		}
	}
	return ""
}

// dockerClient can only send GET requests. There is no code path that
// creates, starts, stops or removes anything.
type dockerClient struct{ http *http.Client }

func newDockerClient(socket string) *dockerClient {
	return &dockerClient{http: &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}}
}

func (d *dockerClient) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	res, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("docker %s: HTTP %d", path, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(into)
}

type dockerListItem struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

type dockerInspect struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		ExitCode  int    `json:"ExitCode"`
		StartedAt string `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type dockerStats struct {
	CPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	Memory struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

func readDocker(ctx context.Context, enabled bool) *Docker {
	socket := dockerSocket()
	if socket == "" {
		return nil
	}
	d := &Docker{Installed: true, Enabled: enabled, Containers: []Container{}}
	if !enabled {
		return d
	}
	client := newDockerClient(socket)
	var list []dockerListItem
	if err := client.get(ctx, "/containers/json?all=1", &list); err != nil {
		d.Error = err.Error()
		return d
	}
	out := make([]Container, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, item := range list {
		out[i] = baseContainer(item)
		wg.Add(1)
		go func(c *Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			enrich(ctx, client, c)
		}(&out[i])
	}
	wg.Wait()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Project != out[b].Project {
			return out[a].Project < out[b].Project
		}
		return out[a].Name < out[b].Name
	})
	d.Containers = out
	return d
}

func baseContainer(item dockerListItem) Container {
	c := Container{
		ID: shortID(item.ID), Image: item.Image, State: item.State, Status: item.Status,
		Project: item.Labels["com.docker.compose.project"], Ports: []Port{},
	}
	if len(item.Names) > 0 {
		c.Name = strings.TrimPrefix(item.Names[0], "/")
	}
	seen := map[string]bool{}
	for _, p := range item.Ports {
		key := fmt.Sprintf("%d/%s/%d", p.PrivatePort, p.Type, p.PublicPort)
		if seen[key] {
			continue
		}
		seen[key] = true
		c.Ports = append(c.Ports, Port{Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type, IP: p.IP})
	}
	return c
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func enrich(ctx context.Context, client *dockerClient, c *Container) {
	id := url.PathEscape(c.ID)
	var in dockerInspect
	if client.get(ctx, "/containers/"+id+"/json", &in) == nil {
		c.RestartCount = in.RestartCount
		c.ExitCode = in.State.ExitCode
		if in.State.Health != nil {
			c.Health = in.State.Health.Status
		}
		if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil && t.Year() > 1 {
			c.StartedAt = t.Unix()
		}
	}
	if c.State != "running" {
		return
	}
	var st dockerStats
	if client.get(ctx, "/containers/"+id+"/stats?stream=false", &st) != nil {
		return
	}
	if dSys := float64(st.CPU.System) - float64(st.PreCPU.System); dSys > 0 {
		dCPU := float64(st.CPU.Usage.Total) - float64(st.PreCPU.Usage.Total)
		// Percent of the whole server, so 100 means all cores busy.
		pct := dCPU / dSys * 100
		c.CPU = &pct
	}
	mem := st.Memory.Usage
	// Leave out the page cache, as `docker stats` does.
	if v, ok := st.Memory.Stats["inactive_file"]; ok && v < mem {
		mem -= v
	} else if v, ok := st.Memory.Stats["total_inactive_file"]; ok && v < mem {
		mem -= v
	}
	c.Memory = &mem
	if st.Memory.Limit > 0 && st.Memory.Limit < 1<<60 {
		limit := st.Memory.Limit
		c.MemoryLimit = &limit
	}
}
