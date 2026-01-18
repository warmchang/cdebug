package containerd

import (
	"context"
	"errors"
	"io"
	"strings"

	ctdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/cmd/ctr/commands/content"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/errdefs"
	"github.com/docker/cli/cli/streams"
)

const (
	defaultNamespace = "default"
)

var wellKnownAddresses = []string{
	"/run/containerd/containerd.sock",
	"/var/run/docker/containerd/containerd.sock",
}

type Client struct {
	*ctdclient.Client
	out       *streams.Out
	namespace string
}

type Options struct {
	Out       *streams.Out
	Address   string
	Namespace string
}

func NewClient(opts Options) (*Client, error) {
	addr, err := detectAddress(opts)
	if err != nil {
		return nil, err
	}

	namespace := defaultNamespace
	if len(opts.Namespace) > 0 {
		namespace = opts.Namespace
	}

	inner, err := ctdclient.New(addr, ctdclient.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, err
	}

	out := opts.Out
	if out == nil {
		out = streams.NewOut(io.Discard)
	}

	return &Client{
		Client:    inner,
		out:       out,
		namespace: namespace,
	}, nil
}

func (c *Client) Namespace() string {
	return c.namespace
}

func (c *Client) ContainerRemoveEx(
	ctx context.Context,
	cont ctdclient.Container,
	force bool,
) error {
	task, err := cont.Task(ctx, cio.Load)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return c.containerRemove(ctx, cont)
		}
		return err
	}

	if err := c.taskRemove(ctx, task, force); err != nil {
		return err
	}

	return c.containerRemove(ctx, cont)
}

func (c *Client) ImagePullEx(
	ctx context.Context,
	ref string,
	platform string,
) (ctdclient.Image, error) {
	if !strings.Contains(ref, ":") {
		ref = ref + ":latest"
	}

	pctx, stopProgress := context.WithCancel(ctx)
	jobs := content.NewJobs(ref)
	progressCh := make(chan struct{})
	go func() {
		content.ShowProgress(pctx, jobs, c.ContentStore(), c.out)
		close(progressCh)
	}()

	image, err := c.Pull(
		ctx,
		ref,
		ctdclient.WithPullUnpack,
		ctdclient.WithPlatform(platform),
	)
	stopProgress()
	if err != nil {
		return image, err
	}

	<-progressCh
	return image, nil
}

func (c *Client) taskRemove(
	ctx context.Context,
	task ctdclient.Task,
	force bool,
) error {
	status, err := task.Status(ctx)
	if err != nil {
		return err
	}

	if status.Status == ctdclient.Created || status.Status == ctdclient.Stopped {
		if _, err := task.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		return nil
	}

	if !force {
		return errors.New("cannot remove active container - stop the container first or try force removal instead")
	}

	if _, err := task.Delete(ctx, ctdclient.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
		return err
	}

	return nil
}

func (c *Client) containerRemove(ctx context.Context, cont ctdclient.Container) error {
	var opts []ctdclient.DeleteOpts
	if _, err := cont.Image(ctx); err == nil {
		opts = append(opts, ctdclient.WithSnapshotCleanup)
	}

	return cont.Delete(ctx, opts...)
}

func detectAddress(opts Options) (string, error) {
	addresses := wellKnownAddresses[:]
	if len(opts.Address) > 0 {
		addresses = []string{strings.TrimPrefix(opts.Address, "unix://")}
	}

	for _, addr := range addresses {
		if isSocketAccessible(addr) == nil {
			return addr, nil
		}
	}

	return "", errors.New("cannot detect (good enough) containerd address")
}
