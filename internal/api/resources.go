package api

import (
	"context"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/events/webhook"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

var typeOfResource = reflect.TypeFor[store.Resource]()

type resourceInput struct {
	ID     string `path:"id" doc:"The resource's ID"`
	Fields string `query:"fields" doc:"Answer with only these fields, as dotted paths separated by commas; id and kind are always included"`
}

type resourceOutput struct {
	Body any
}

type streamResourcesInput struct {
	Kinds []string `query:"kind" explode:"true" doc:"Only resources of these kinds; repeat it for several"`
	Scope string   `query:"scope" doc:"Only resources of one owner: server, reseller:<id> or account:<id>"`
	IDs   []string `query:"id" explode:"true" doc:"Only these resources; repeat it for several"`
}

// followResources sends the notices of w that the client may see until the
// watcher closes or ctx is done.
func followResources(ctx context.Context, w *store.Watcher, visible func(store.Notice) bool, out send) error {
	var dropped int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case n, ok := <-w.C:
			if !ok {
				return nil
			}
			// Notices that didn't fit can't be read back, so the client
			// learns it has to read its lists again.
			if d := w.Dropped(); d != dropped {
				if err := out("resync", "", ResyncNotice{Reason: "lagging"}); err != nil {
					return err
				}
				dropped = d
			}
			if !visible(n) {
				continue
			}
			if err := out("notice", "", n); err != nil {
				return err
			}
		}
	}
}

// ResyncNotice tells a client of the resource stream that it read too slowly
// to be told about every change, so what it shows may be out of date. It
// doesn't say how many notices were lost, since some may have been about
// resources the client can't see.
type ResyncNotice struct {
	Reason string `json:"reason" enum:"lagging" doc:"Why to read the lists again: lagging, the client didn't keep up"`
}

type deliveriesInput struct {
	ID     string                `path:"id" doc:"The webhook's ID"`
	State  webhook.DeliveryState `query:"state" enum:"pending,succeeded,failed" doc:"Only deliveries in this state"`
	Cursor string                `query:"cursor" doc:"Continues a list where the page before it ended"`
	Limit  int                   `query:"limit" minimum:"0" maximum:"1000" doc:"How many to return; 50 when left out"`
}

type deliveriesOutput struct {
	Body struct {
		Deliveries []*webhook.Delivery `json:"deliveries" doc:"The deliveries on this page, newest first"`
		Next       string              `json:"next,omitempty" doc:"The cursor of the next page; empty on the last one"`
	}
}

type deliveryInput struct {
	ID       string   `path:"id" doc:"The webhook's ID"`
	Delivery store.ID `path:"delivery" doc:"The delivery's ID, which starts with dlv_"`
}

type deliveryOutput struct {
	Body *webhook.Delivery
}

func (s *Server) registerResources() {
	op := huma.Operation{
		OperationID: "getResource", Method: http.MethodGet, Path: Prefix + "/resources/{id}", Tags: []string{"System"}, Security: Protected,
		Summary:     "Get any resource by its ID",
		Description: "Returns the resource an ID names, whatever its kind; the ID's prefix says the kind. Handy for links and mentions that carry only an ID.",
		Responses:   map[string]*huma.Response{"200": jsonResponse("The resource", s.schemaOf(typeOfResource, "Resource"))},
	}
	Describe(&op, Info{Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *resourceInput) (*resourceOutput, error) {
		fields, err := parseFields(params{"fields": {in.Fields}})
		if err != nil {
			return nil, err
		}
		id, err := store.ParseID(in.ID)
		if err != nil {
			return nil, store.ErrNotFound.New("what", in.ID)
		}
		k, ok := s.kinds.ForID(id)
		if !ok {
			return nil, store.ErrNotFound.New("what", in.ID)
		}
		r, err := s.readable(ctx, k.Info(), in.ID)
		if err != nil {
			return nil, err
		}
		out, err := fields.apply(r)
		if err != nil {
			return nil, err
		}
		return &resourceOutput{Body: out}, nil
	})

	op = huma.Operation{
		OperationID: "streamResources", Method: http.MethodGet, Path: Prefix + "/resources/stream", Tags: []string{"System"}, Security: Protected,
		Summary: "Follow changes to resources",
		Description: "Streams a notice, as Server-Sent Events, every time a resource you can see is created, changed, deleted or purged, or its status changes, " +
			"so screens stay current without asking again. Notices name the resource and its new phase; read it again for the rest. " +
			"After a reconnect, or a resync event, which says the client fell behind and missed notices, read the lists again.",
	}
	Describe(&op, Info{Risk: store.RiskRead, Stream: true})
	registerStream(s, op, map[string]any{"notice": store.Notice{}, "resync": ResyncNotice{}}, func(ctx context.Context, in *streamResourcesInput) (stream, error) {
		if _, err := Actor(ctx); err != nil {
			return nil, err
		}
		st := s.store()
		if st == nil {
			return nil, i18n.ErrUnavailable.New("what", "The store")
		}
		filter := store.WatchFilter{Kinds: in.Kinds}
		for _, kind := range in.Kinds {
			if _, ok := s.kinds.Get(kind); !ok {
				return nil, i18n.ErrUnknownName.New("what", "kind", "name", kind)
			}
		}
		if in.Scope != "" {
			sc, err := store.ParseScope(in.Scope)
			if err != nil {
				return nil, badParam("scope", "use server, reseller:<id> or account:<id>")
			}
			filter.Scopes = []store.Scope{sc}
		}
		for _, raw := range in.IDs {
			id, err := store.ParseID(raw)
			if err != nil {
				return nil, badParam("id", raw+" isn't an ID")
			}
			filter.IDs = append(filter.IDs, id)
		}
		return func(ctx context.Context, out send, ready func() error) error {
			w := st.Watch(filter, 256)
			defer w.Close()
			if err := ready(); err != nil {
				return err
			}
			return followResources(ctx, w, func(n store.Notice) bool {
				return canRead(ctx, s.authorizer(), store.Meta{ID: n.ID, Kind: n.Kind, Scope: n.Scope})
			}, out)
		}, nil
	})

	if _, ok := s.kinds.Get(webhook.Kind.Name()); !ok {
		return
	}
	base := KindPath(webhook.Kind.Info())
	tags := []string{s.tagOf(webhook.Kind.Info())}
	op = huma.Operation{
		OperationID: "listCoreWebhookDeliveries", Method: http.MethodGet, Path: base + "/{id}/deliveries", Tags: tags, Security: Protected,
		Summary:     "List a webhook's deliveries",
		Description: "Lists the events sent, or being sent, to a webhook, newest first, with how many attempts each took and how the last one went.",
	}
	Describe(&op, Info{Kind: webhook.Kind.Name(), Operation: "deliveries", Permission: string(ReadPermission(webhook.Kind.Name())), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *deliveriesInput) (*deliveriesOutput, error) {
		r, err := s.readable(ctx, webhook.Kind.Info(), in.ID)
		if err != nil {
			return nil, err
		}
		d := s.opts.Webhooks
		if d == nil {
			return nil, i18n.ErrUnavailable.New("what", "Webhook deliveries")
		}
		list, next, err := d.Deliveries(ctx, r.ID, in.State, in.Cursor, in.Limit)
		if err != nil {
			return nil, err
		}
		out := &deliveriesOutput{}
		out.Body.Deliveries = append([]*webhook.Delivery{}, list...)
		out.Body.Next = next
		return out, nil
	})
	op = huma.Operation{
		OperationID: "getCoreWebhookDelivery", Method: http.MethodGet, Path: base + "/{id}/deliveries/{delivery}", Tags: tags, Security: Protected,
		Summary:     "Get a webhook delivery",
		Description: "Returns one delivery with the body that was sent and every attempt: when, how the receiver answered, and the start of its answer.",
	}
	Describe(&op, Info{Kind: webhook.Kind.Name(), Operation: "delivery", Permission: string(ReadPermission(webhook.Kind.Name())), Risk: store.RiskRead})
	huma.Register(s.api, op, func(ctx context.Context, in *deliveryInput) (*deliveryOutput, error) {
		r, err := s.readable(ctx, webhook.Kind.Info(), in.ID)
		if err != nil {
			return nil, err
		}
		d := s.opts.Webhooks
		if d == nil {
			return nil, i18n.ErrUnavailable.New("what", "Webhook deliveries")
		}
		dl, err := d.Delivery(ctx, r.ID, in.Delivery)
		if err != nil {
			return nil, err
		}
		return &deliveryOutput{Body: dl}, nil
	})
}
