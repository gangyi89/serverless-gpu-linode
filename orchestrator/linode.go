package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/linode/linodego"
	"golang.org/x/oauth2"
)

// CloudProvider abstracts GPU node lifecycle operations.
// LinodeClient is the production implementation; tests use a fake.
type CloudProvider interface {
	CreateGPUNode(ctx context.Context, label string) (id int, err error)
	WaitForRunning(ctx context.Context, id int, timeout time.Duration) (ip string, err error)
	DestroyNode(ctx context.Context, id int) error
	ListManagedNodes(ctx context.Context) ([]CloudNode, error)
}

// CloudNode is the cloud-provider view of a managed GPU node.
type CloudNode struct {
	LinodeID int
	Label    string
	IPv4     string
	Status   string
}

// LinodeClient implements CloudProvider using the Linode API.
type LinodeClient struct {
	client       linodego.Client
	imageID      string
	region       string
	instanceType string
	rootPass     string
	managedTags  []string
}

func NewLinodeClient(token, imageID, region, instanceType, rootPass string, managedTags []string) *LinodeClient {
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauthClient := &http.Client{
		Transport: &oauth2.Transport{Source: tokenSource},
	}
	client := linodego.NewClient(oauthClient)

	return &LinodeClient{
		client:       client,
		imageID:      imageID,
		region:       region,
		instanceType: instanceType,
		rootPass:     rootPass,
		managedTags:  managedTags,
	}
}

func (l *LinodeClient) CreateGPUNode(ctx context.Context, label string) (int, error) {
	slog.Info("creating GPU node",
		"label", label,
		"region", l.region,
		"type", l.instanceType,
		"image", l.imageID,
	)

	booted := true
	instance, err := l.client.CreateInstance(ctx, linodego.InstanceCreateOptions{
		Label:    label,
		Region:   l.region,
		Type:     l.instanceType,
		Image:    l.imageID,
		RootPass: l.rootPass,
		Booted:   &booted,
		Tags:     l.managedTags,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create instance: %w", err)
	}

	slog.Info("GPU node created", "linode_id", instance.ID, "label", label)
	return instance.ID, nil
}

func (l *LinodeClient) WaitForRunning(ctx context.Context, linodeID int, timeout time.Duration) (string, error) {
	slog.Info("waiting for node to reach running state", "linode_id", linodeID)

	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("timeout waiting for node %d to reach running state", linodeID)
		case <-ticker.C:
			instance, err := l.client.GetInstance(ctx, linodeID)
			if err != nil {
				slog.Warn("failed to poll node status", "linode_id", linodeID, "error", err)
				continue
			}

			if instance.Status == linodego.InstanceRunning {
				ip := ""
				if len(instance.IPv4) > 0 {
					ip = instance.IPv4[0].String()
				}
				slog.Info("node is running", "linode_id", linodeID, "ip", ip)
				return ip, nil
			}
		}
	}
}

func (l *LinodeClient) DestroyNode(ctx context.Context, linodeID int) error {
	slog.Info("destroying GPU node", "linode_id", linodeID)
	if err := l.client.DeleteInstance(ctx, linodeID); err != nil {
		return fmt.Errorf("failed to destroy node %d: %w", linodeID, err)
	}
	slog.Info("GPU node destroyed", "linode_id", linodeID)
	return nil
}

func (l *LinodeClient) ListManagedNodes(ctx context.Context) ([]CloudNode, error) {
	instances, err := l.client.ListInstances(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list instances: %w", err)
	}

	nodes := make([]CloudNode, 0, len(instances))
	for _, inst := range instances {
		if !containsAllTags(inst.Tags, l.managedTags) {
			continue
		}

		ip := ""
		if len(inst.IPv4) > 0 {
			ip = inst.IPv4[0].String()
		}

		nodes = append(nodes, CloudNode{
			LinodeID: inst.ID,
			Label:    inst.Label,
			IPv4:     ip,
			Status:   string(inst.Status),
		})
	}

	return nodes, nil
}

func containsAllTags(instanceTags, requiredTags []string) bool {
	for _, required := range requiredTags {
		found := false
		for _, tag := range instanceTags {
			if tag == required {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
