# TypeScript SDK

This directory is the add-on SDK for TypeScript on Node.js, published as `@racksail/addon-sdk`. Its types are to be generated from [spec/addon-protocol.json](../../spec/addon-protocol.json) and [spec/openapi.json](../../spec/openapi.json) with openapi-typescript, as the panel's UI does.

```ts
import { supports } from "@racksail/addon-sdk";

if (!supports("v1")) {
  throw new Error("this SDK doesn't speak the hook API version the add-on was written for");
}
```

`apiVersions` lists the hook API versions the SDK speaks, newest first; a test keeps the newest equal to the protocol document's version. Serving the protocol, the hook types, the API client, reading `addon.yaml` and the local test harness build on it, as [the SDK overview](../README.md) describes.

```sh
cd sdk/ts
npm ci
npm run typecheck
npm test
npm run build      # writes dist/
```
