import { defineConfig } from 'astro/config';

export default defineConfig({
  site: process.env.SITE_URL ?? 'https://mem9.ai',
  base: process.env.SITE_BASE ?? '/',
  output: 'static',
});
