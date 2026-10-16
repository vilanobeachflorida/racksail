# TypeScript SDK

This directory is the add-on SDK for TypeScript on Node.js, published as `@racksail/addon-sdk`. It follows the add-on protocol in [spec/addon-protocol.json](../../spec/addon-protocol.json).

```ts
import { supports } from "@racksail/addon-sdk";

if (!supports("v1")) {
  throw new Error("this SDK doesn't speak the hook API version the add-on was written for");
}
```

`apiVersions` lists the hook API versions the SDK speaks, newest first, and `supports` says whether it speaks one; a test keeps the newest equal to the protocol document's version. That is all the SDK has: an add-on serves the protocol itself, as [the SDK overview](../README.md) describes.

```sh
cd sdk/ts
npm ci
npm run typecheck
npm test
npm run build      # writes dist/
```
