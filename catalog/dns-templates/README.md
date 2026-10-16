# DNS templates

The records a service needs in a zone, such as the MX, SPF, DKIM and autodiscover records for Google Workspace, Microsoft 365, Fastmail, Proton, Zoho or iCloud. A template's recipe takes the zone as a `resource` input of kind `dns.zone` and creates `dns.record` resources in it, so applying a template is one change you can preview and undo.

Rules for DNS templates, besides the catalog's:

- Values come from the provider's own documentation, linked from `homepage`.
- Inputs cover what differs per customer, such as a verification code or a DKIM selector, with a `pattern` when the provider's format is fixed.
- A `resource` check of the records it creates, and an install test, with a zone id as its zone.
- `requires.modules` includes `dns`.

```
dns-templates/fastmail/
  recipe.yaml
  icon.svg
```
