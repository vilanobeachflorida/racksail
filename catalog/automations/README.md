# Automations

Ready-made workflows: staged WordPress updates, uptime alerts, a monthly restore test, a certificate watch and the like. An automation's recipe creates an `automations.workflow` resource with its triggers and steps, filled in from the inputs, such as the site to watch or how often.

Rules for automations, besides the catalog's:

- Every step is an API action, a condition, a wait, an approval, a notification or a recipe run; nothing runs commands.
- Steps that change something run as change sets, so they're previewed and recorded like any change.
- An install test with values for every required input.
- `requires.modules` includes `automations` and every module the steps use.

```
automations/uptime-alerts/
  recipe.yaml
  icon.svg
```
