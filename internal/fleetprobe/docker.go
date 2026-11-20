package fleetprobe

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// dockerContainer is the subset of `docker inspect` (container) the probe reads.
// Config.Env is deliberately absent: environment values are never read.
type dockerContainer struct {
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status    string    `json:"Status"`
		StartedAt time.Time `json:"StartedAt"`
		Pid       int       `json:"Pid"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// dockerImage is the subset of `docker image inspect` the probe reads.
type dockerImage struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	Created     string   `json:"Created"`
	Config      struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

var errNoDocker = errors.New("docker not available")

func dockerCmd(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, errNoDocker
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, path, args...).Output()
}

// containers inspects every running container and its image.
func containers(ctx context.Context) ([]Service, error) {
	ids, err := dockerCmd(ctx, "ps", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	idList := strings.Fields(string(ids))
	if len(idList) == 0 {
		return nil, nil
	}
	raw, err := dockerCmd(ctx, append([]string{"inspect"}, idList...)...)
	if err != nil {
		return nil, err
	}
	var cs []dockerContainer
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, err
	}
	imgIDs := map[string]bool{}
	for _, c := range cs {
		imgIDs[c.Image] = true
	}
	images := map[string]dockerImage{}
	if len(imgIDs) > 0 {
		args := []string{"image", "inspect"}
		for id := range imgIDs {
			args = append(args, id)
		}
		if raw, err := dockerCmd(ctx, args...); err == nil {
			var is []dockerImage
			if json.Unmarshal(raw, &is) == nil {
				for _, im := range is {
					images[im.ID] = im
				}
			}
		}
	}
	return containerServices(cs, images), nil
}

func containerServices(cs []dockerContainer, images map[string]dockerImage) []Service {
	out := make([]Service, 0, len(cs))
	for _, c := range cs {
		s := Service{Kind: KindContainer, Name: strings.TrimPrefix(c.Name, "/"), PID: c.State.Pid}
		if !c.State.StartedAt.IsZero() {
			s.StartedAt = c.State.StartedAt.UTC().Truncate(time.Second)
		}
		img := &Image{Ref: c.Config.Image, ID: c.Image, State: c.State.Status}
		if c.State.Health != nil {
			img.Health = c.State.Health.Status
		}
		if im, ok := images[c.Image]; ok {
			img.Digests = im.RepoDigests
			if t, err := time.Parse(time.RFC3339Nano, im.Created); err == nil {
				img.Created = t.UTC().Truncate(time.Second)
			}
			img.Version = im.Config.Labels["org.opencontainers.image.version"]
			img.Revision = im.Config.Labels["org.opencontainers.image.revision"]
			img.Source = im.Config.Labels["org.opencontainers.image.source"]
		}
		s.Image = img
		if p := c.Config.Labels["com.docker.compose.project"]; p != "" {
			s.Compose = &Compose{Project: p, Service: c.Config.Labels["com.docker.compose.service"],
				WorkingDir: c.Config.Labels["com.docker.compose.project.working_dir"]}
		}
		out = append(out, s)
	}
	return out
}
