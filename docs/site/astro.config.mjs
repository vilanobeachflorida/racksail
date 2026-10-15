import starlight from "@astrojs/starlight";
import { defineConfig } from "astro/config";

const repository = "https://github.com/vilanobeachflorida/racksail";

// The sidebar follows the docs plan in docs/architecture/features.md,
// section 36. Every module has a page under modules/, named by its id.
const sidebar = [
  {
    label: "Getting started",
    items: [
      "getting-started/introduction",
      "getting-started/quick-start",
      "getting-started/concepts",
      "getting-started/panel-tour",
      "getting-started/setup-wizard",
      "getting-started/try-without-a-server",
    ],
  },
  {
    label: "Install",
    items: [
      "install/requirements",
      "install/iso",
      "install/browser",
      "install/unattended",
      "install/existing-server",
      "install/cloud-images",
      "install/console",
      "install/updates",
    ],
  },
  {
    label: "Modules",
    items: [
      "modules/overview",
      "modules/network",
      "modules/web",
      "modules/hosting",
      "modules/databases",
      "modules/dns",
      "modules/mail",
      "modules/containers",
      "modules/vms",
      "modules/storage",
      "modules/shares",
      "modules/home",
      "modules/marketplace",
      "modules/backups",
      "modules/monitoring",
      "modules/files",
      "modules/performance",
      "modules/builder",
      "modules/ai",
      "modules/automations",
      "modules/migration",
      "modules/cluster",
      "modules/security",
    ],
  },
  {
    label: "Migration guides",
    items: [
      "migration/overview",
      "migration/cpanel",
      "migration/plesk",
      "migration/directadmin",
      "migration/cwp",
      "migration/hestiacp",
      "migration/unraid",
      "migration/ssh",
    ],
  },
  {
    label: "Recipes",
    items: [
      "recipes/overview",
      "recipes/format",
      "recipes/testing",
      "recipes/catalogs",
    ],
  },
  {
    label: "Add-ons",
    items: [
      "addons/overview",
      "addons/hooks",
      "addons/rules",
      "addons/manifest",
      "addons/permissions",
      "addons/ui-extensions",
      "addons/sdks",
      "addons/publishing",
    ],
  },
  {
    label: "Themes",
    items: [
      "themes/overview",
      "themes/format",
      "themes/layers",
      "themes/editor",
    ],
  },
  {
    label: "API reference",
    items: [
      "api/overview",
      "api/reference",
      "api/cli",
      "api/webhooks",
      "api/clients",
    ],
  },
  {
    label: "Architecture",
    items: [
      "architecture/overview",
      "architecture/change-pipeline",
      "architecture/agent",
      "architecture/system-layer",
      "architecture/edge",
      "architecture/secrets",
      "architecture/modules",
      "architecture/development",
    ],
  },
  {
    label: "Security",
    items: [
      "security/model",
      "security/users-and-roles",
      "security/sign-in",
      "security/api-tokens",
      "security/hardening",
      "security/reporting",
    ],
  },
  {
    label: "Brand",
    items: [
      "brand/logo",
      "brand/colors",
      "brand/typography",
      "brand/voice",
      "brand/ui-patterns",
      "brand/applications",
    ],
  },
  {
    label: "Community",
    items: [
      "community/contributing",
      "community/translations",
      "community/governance",
      "community/code-of-conduct",
    ],
  },
  { slug: "faq" },
];

export default defineConfig({
  // Published on GitHub Pages as a project site, so every page lives under
  // /racksail/. Links between pages are relative, so they work wherever the
  // site is served from.
  site: "https://vilanobeachflorida.github.io",
  base: "/racksail",
  integrations: [
    starlight({
      title: "Racksail",
      description: "Documentation for Racksail, the open source server platform. Your server, your rules.",
      logo: {
        light: "./src/assets/mark-light.svg",
        dark: "./src/assets/mark-dark.svg",
      },
      favicon: "/favicon.svg",
      social: [{ icon: "github", label: "GitHub", href: repository }],
      editLink: { baseUrl: `${repository}/edit/main/docs/site/` },
      customCss: [
        "@fontsource-variable/inter",
        "@fontsource-variable/jetbrains-mono",
        "./src/styles/brand.css",
      ],
      lastUpdated: false,
      // src/pages/404.astro is the 404 page. Starlight's own would look for a
      // docs entry called 404, which would also be published as /404/.
      disable404Route: true,
      sidebar,
    }),
  ],
});
