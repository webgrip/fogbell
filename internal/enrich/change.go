package enrich

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/upstream"
)

// KubeReader reads the Kubernetes API.
type KubeReader interface {
	Get(ctx context.Context, path string, out any) error
}

var fluxLists = []struct{ kind, path string }{
	{"Kustomization", "/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations"},
	{"HelmRelease", "/apis/helm.toolkit.fluxcd.io/v2/helmreleases"},
}

// FluxObject is the part of a Kustomization or HelmRelease the change line reads.
type FluxObject struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		TargetNamespace string `json:"targetNamespace"`
		Path            string `json:"path"`
		SourceRef       struct {
			Name string `json:"name"`
		} `json:"sourceRef"`
	} `json:"spec"`
	Status struct {
		History []struct {
			FirstReconciled      string `json:"firstReconciled"`
			TotalReconciliations int    `json:"totalReconciliations"`
			Metadata             struct {
				Revision string `json:"revision"`
			} `json:"metadata"`
			LastDeployed string `json:"lastDeployed"`
			ChartName    string `json:"chartName"`
			ChartVersion string `json:"chartVersion"`
		} `json:"history"`
	} `json:"status"`
}

func (o FluxObject) target() string {
	if o.Spec.TargetNamespace != "" {
		return o.Spec.TargetNamespace
	}
	return o.Metadata.Namespace
}

// Change is one change Flux applied into a namespace.
type Change struct {
	Kind, Name, What, SHA, Path string
	At                          float64
}

// FluxChanges is every change Flux applied into a namespace. An entry's revision is the latest commit
// that reconciled that content, not the one that brought it, so a commit is named only for an entry
// reconciled once; otherwise the change is the Kustomization's path.
func FluxChanges(items map[string][]FluxObject, namespace, ownSource string) []Change {
	var out []Change
	for _, ks := range items["Kustomization"] {
		if ks.target() != namespace {
			continue
		}
		own := ks.Spec.SourceRef.Name == ownSource
		path := strings.Trim(strings.TrimPrefix(ks.Spec.Path, "./"), "/")
		for _, h := range ks.Status.History {
			rev := h.Metadata.Revision
			sha := ""
			if i := strings.LastIndex(rev, "sha1:"); i >= 0 && h.TotalReconciliations <= 1 {
				sha = rev[i+5:]
			}
			c := Change{Kind: "Kustomization", Name: ks.Metadata.Name, At: alert.ParseTime(h.FirstReconciled)}
			switch {
			case sha != "":
				c.What = sha[:min(8, len(sha))]
			case own && path != "":
				c.What = path
			default:
				c.What = rev
				if i := strings.LastIndex(rev, "@"); i > 0 {
					c.What = rev[:i]
				}
			}
			if own {
				c.SHA, c.Path = sha, path
			}
			out = append(out, c)
		}
	}
	for _, hr := range items["HelmRelease"] {
		if hr.target() != namespace {
			continue
		}
		for _, h := range hr.Status.History {
			out = append(out, Change{Kind: "HelmRelease", Name: hr.Metadata.Name, At: alert.ParseTime(h.LastDeployed),
				What: strings.TrimSpace(h.ChartName + " " + h.ChartVersion)})
		}
	}
	return out
}

// Links turn a change into a link.
type Links struct {
	Commit, Path string
}

// Line is "🔧 Kustomization `app` applied `1a2b3c4d` 12 min before this alert".
func (c Change) Line(start float64, links Links) string {
	before := format.Duration(math.Max(60, start-c.At))
	what := "`" + format.Escape(c.What) + "`"
	verb := "applied"
	if c.Kind == "HelmRelease" {
		verb = "upgraded to"
	}
	switch {
	case c.SHA != "" && links.Commit != "":
		what = "<" + links.Commit + c.SHA + "|" + format.Escape(c.What) + ">"
	case c.Path != "":
		verb = "applied a change in"
		if links.Path != "" {
			what = "<" + links.Path + c.Path + "|" + format.Escape(c.Path) + ">"
		}
	}
	return "🔧 " + c.Kind + " `" + format.Escape(c.Name) + "` " + verb + " " + what + " " + before + " before this alert"
}

// Flux names the latest change Flux applied into the alert's namespace within Window before it
// started.
type Flux struct {
	Kube      KubeReader
	Window    time.Duration
	OwnSource string
	Links     Links
	Now       func() time.Time

	mu    sync.Mutex
	at    time.Time
	items map[string][]FluxObject
}

// Kind implements Enricher.
func (*Flux) Kind() string { return "change" }

// Slot implements Line.
func (*Flux) Slot() Slot { return SlotChange }

// Enrich implements Enricher.
func (f *Flux) Enrich(ctx context.Context, sub *Subject) (Result, error) {
	active := sub.Group.Active()
	namespaces := map[string]bool{}
	for _, a := range active {
		namespaces[a.Labels["namespace"]] = true
	}
	if len(namespaces) != 1 || namespaces[""] {
		return outcome(Skipped)
	}
	var ns string
	for n := range namespaces {
		ns = n
	}
	items, err := f.list(ctx)
	if err != nil {
		var se *upstream.StatusError
		if errors.As(err, &se) && (se.Code == 401 || se.Code == 403) {
			return outcome(Denied)
		}
		return Result{}, err
	}
	start := earliestStart(sub.Group, float64(f.Now().Unix()))
	var best *Change
	for _, c := range FluxChanges(items, ns, f.OwnSource) {
		if c.At >= start-f.Window.Seconds() && c.At <= start && (best == nil || c.At > best.At) {
			best = &c
		}
	}
	if best == nil {
		return outcome(None)
	}
	return line(OK, best.Line(start, f.Links))
}

func (f *Flux) list(ctx context.Context) (map[string][]FluxObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items != nil && f.Now().Sub(f.at) < time.Minute {
		return f.items, nil
	}
	items := map[string][]FluxObject{}
	for _, l := range fluxLists {
		var list struct {
			Items []FluxObject `json:"items"`
		}
		if err := f.Kube.Get(ctx, l.path, &list); err != nil {
			return nil, err
		}
		items[l.kind] = list.Items
	}
	f.items, f.at = items, f.Now()
	return items, nil
}
