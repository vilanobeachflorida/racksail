---
title: "Go and TypeScript clients"
description: "Call the API from your own code, with a client generated from its OpenAPI document."
---

The API is described by an OpenAPI 3.1 document, generated from the code that serves it, so a client generated from it knows every operation, parameter and schema of the panel you run. The panel's own web UI is built that way. This page shows how to do the same in TypeScript and in Go; [Using the API](../overview/) covers what every operation has in common.

## Get the document

Every panel serves the document of its own version, without signing in:

```sh
curl --fail -o racksail.json https://panel.example.com:8443/api/v1/openapi.json
```

`racksail openapi > racksail.json` prints the same document from the binary, with no panel at all, and the repository keeps it as [spec/openapi.json](https://github.com/vilanobeachflorida/racksail/blob/main/spec/openapi.json). Generate your client from the version your panels run, and again when you update them, so it knows what the new version adds.

Besides the usual OpenAPI fields, each operation says what it is to Racksail in `x-racksail-` extensions, which a client can use to warn before risky calls:

| Extension | Says |
|---|---|
| `x-racksail-kind`, `x-racksail-operation` | The kind and operation, such as `core.webhook` and `create` |
| `x-racksail-permission` | The permission it needs, such as `core.webhook.create` |
| `x-racksail-risk` | `read`, `low`, `medium`, `high` or `critical` |
| `x-racksail-dry-run` | It takes `?dryRun=true` and answers with a preview |
| `x-racksail-destructive`, `x-racksail-reversible` | It removes data; it can be undone |
| `x-racksail-long` | It runs as a job, and answers before the job is done |
| `x-racksail-stream` | It answers with Server-Sent Events |
| `x-racksail-session` | Only a person in the panel may call it, never a token |
| `x-racksail-step-up` | The person has to confirm it's them first |

## TypeScript

The web UI generates its types with [openapi-typescript](https://openapi-ts.dev/) and calls the API with openapi-fetch, which checks every path, parameter and body against them as you type:

```sh
npm install openapi-fetch
npm install --save-dev openapi-typescript
npx openapi-typescript racksail.json --output src/racksail.d.ts
```

```ts
import createClient from "openapi-fetch";
import type { paths } from "./racksail";

const racksail = createClient<paths>({
  baseUrl: "https://panel.example.com:8443",
  headers: { Authorization: `Bearer ${process.env.RACKSAIL_TOKEN}` },
});

// Every webhook, a page at a time.
let cursor: string | undefined;
do {
  const { data, error } = await racksail.GET("/api/v1/core/webhooks", {
    params: { query: { limit: 50, cursor } },
  });
  if (error) {
    throw new Error(`${error.code}: ${error.detail}`);
  }
  for (const hook of data.items) {
    console.log(hook.id, hook.spec.url);
  }
  cursor = data.next || undefined;
} while (cursor);

// Preview a new webhook, then create it. The idempotency key makes a retry
// after a dropped connection return the first change set rather than make
// a second webhook.
const body = { spec: { url: "https://hooks.example.com/racksail", events: ["*.failed"] } };
const preview = await racksail.POST("/api/v1/core/webhooks", { params: { query: { dryRun: true } }, body });
console.log(preview.data?.changes);
const created = await racksail.POST("/api/v1/core/webhooks", {
  params: { header: { "Idempotency-Key": crypto.randomUUID() } },
  body,
});
console.log(created.data?.id, created.data?.job);
```

Errors come back as `error`, typed as the API's problem details, so `error.code` is the stable code to match on.

## Go

The standard library is enough to call the API, with types of your own for the parts you use:

```go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
)

const panel = "https://panel.example.com:8443"

// problem is the part of the API's problem details worth acting on.
type problem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (p problem) Error() string { return p.Code + ": " + p.Detail }

// call sends a request to the API and decodes the answer into out, or
// returns the problem the panel answered with.
func call(method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, panel+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("RACKSAIL_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var p problem
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return fmt.Errorf("the panel answered %s", resp.Status)
		}
		return p
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func main() {
	for cursor := ""; ; {
		var page struct {
			Items []struct {
				ID   string `json:"id"`
				Spec struct {
					URL string `json:"url"`
				} `json:"spec"`
			} `json:"items"`
			Next string `json:"next"`
		}
		if err := call(http.MethodGet, "/api/v1/core/webhooks?limit=50&cursor="+url.QueryEscape(cursor), nil, &page); err != nil {
			log.Fatal(err)
		}
		for _, hook := range page.Items {
			fmt.Println(hook.ID, hook.Spec.URL)
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
}
```

For a typed client of the whole API, run a code generator that reads OpenAPI 3.1 on the document. Either way, match errors on their `code`, never on their English `detail`.

## What every client should do

- **Sign in with an API token** of its own, with only the permissions and accounts it needs; see [API tokens](../../security/api-tokens/). Operations marked `x-racksail-session` refuse tokens.
- **Preview first** where it matters: `?dryRun=true` answers with the change set, what it would change on the server, without applying it.
- **Send an `Idempotency-Key`** with every change, so a retry after a timeout returns the first change set instead of making the change twice.
- **Follow the job.** A change answers `202 Accepted` with its change set once its settings are written, and the change set's `job` carries out the rest on the server. `GET /api/v1/jobs/{id}` says how it went; see [Using the API](../overview/).
- **Page through lists** with `cursor` and `limit` until `next` is empty.
- **Slow down when asked.** A `429` answer says in `Retry-After` how many seconds to wait.
