import { docsLoader, i18nLoader } from "@astrojs/starlight/loaders";
import { docsSchema, i18nSchema } from "@astrojs/starlight/schema";
import { defineCollection } from "astro:content";

export const collections = {
  docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
  // Starlight's interface strings, reworded where the panel says things
  // differently, so the docs and the panel read the same.
  i18n: defineCollection({ loader: i18nLoader(), schema: i18nSchema() }),
};
