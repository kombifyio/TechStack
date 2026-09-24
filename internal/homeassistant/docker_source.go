package homeassistant

import (
	"context"
	"errors"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// DockerSourceGrant is a local administrator's exact existing-container grant.
// It permits power operations only; missing containers are never recreated.
type DockerSourceGrant struct {
	Endpoint    string            `json:"endpoint"`
	ContainerID string            `json:"container_id"`
	Name        string            `json:"name"`
	ImageID     string            `json:"image_id"`
	Labels      map[string]string `json:"labels"`
}
type SourceLifecycle interface {
	Stop(context.Context) (bool, error)
	Start(context.Context) (bool, error)
	Stopped(context.Context) (bool, error)
	VerifyNoRadios(context.Context) error
}
type DockerSource struct {
	cli   *client.Client
	grant DockerSourceGrant
}

var immutableDockerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func NewDockerSource(grant DockerSourceGrant) (*DockerSource, error) {
	if (!strings.HasPrefix(grant.Endpoint, "unix:///") && !strings.HasPrefix(grant.Endpoint, "npipe:////./pipe/")) || !immutableDockerID.MatchString(grant.ContainerID) || !strings.HasPrefix(grant.ImageID, "sha256:") || !immutableDockerID.MatchString(strings.TrimPrefix(grant.ImageID, "sha256:")) || grant.Name == "" || grant.Labels == nil {
		return nil, ErrUnauthorized
	}
	cli, err := client.New(client.WithHost(grant.Endpoint), client.WithTimeout(45*time.Second))
	if err != nil {
		return nil, err
	}
	return &DockerSource{cli: cli, grant: grant}, nil
}
func (d *DockerSource) inspect(ctx context.Context) (container.InspectResponse, error) {
	out, err := d.cli.ContainerInspect(ctx, d.grant.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		return out.Container, err
	}
	c := out.Container
	if c.ID != d.grant.ContainerID || strings.TrimPrefix(c.Name, "/") != strings.TrimPrefix(d.grant.Name, "/") || c.Image != d.grant.ImageID || c.Config == nil || !reflect.DeepEqual(c.Config.Labels, d.grant.Labels) || c.State == nil || c.HostConfig == nil {
		return c, ErrUnauthorized
	}
	if c.HostConfig.AutoRemove || c.State.Dead || c.State.Restarting || c.State.Paused {
		return c, errors.New("source container cannot safely retain its original lifecycle")
	}
	return c, nil
}
func (d *DockerSource) VerifyNoRadios(ctx context.Context) error {
	c, err := d.inspect(ctx)
	if err != nil {
		return err
	}
	if c.HostConfig.Privileged || len(c.HostConfig.Devices) > 0 || len(c.HostConfig.DeviceRequests) > 0 || len(c.HostConfig.DeviceCgroupRules) > 0 {
		return errors.New("container radio passthrough requires a separately authorized transfer adapter")
	}
	for _, m := range c.Mounts {
		if m.Source == "/dev" || strings.HasPrefix(m.Source, "/dev/") || m.Destination == "/dev" || strings.HasPrefix(m.Destination, "/dev/") {
			return errors.New("container device mount cannot be migrated implicitly")
		}
	}
	return nil
}
func (d *DockerSource) Stopped(ctx context.Context) (bool, error) {
	c, err := d.inspect(ctx)
	return err == nil && !c.State.Running, err
}
func (d *DockerSource) Stop(ctx context.Context) (bool, error) {
	c, err := d.inspect(ctx)
	if err != nil {
		return false, err
	}
	if !c.State.Running {
		return true, nil
	}
	timeout := 30
	if _, err = d.cli.ContainerStop(ctx, d.grant.ContainerID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		return false, err
	}
	return d.Stopped(ctx)
}
func (d *DockerSource) Start(ctx context.Context) (bool, error) {
	c, err := d.inspect(ctx)
	if err != nil {
		return false, err
	}
	if c.State.Running {
		return true, nil
	}
	if _, err = d.cli.ContainerStart(ctx, d.grant.ContainerID, client.ContainerStartOptions{}); err != nil {
		return false, err
	}
	c, err = d.inspect(ctx)
	return err == nil && c.State.Running, err
}
