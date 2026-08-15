# Testing notes — Playwright

This short note explains how to run the Playwright smoke tests in this repo (headed vs headless).

Prerequisites

- Backend running (recommended: Docker): `docker compose up -d`
- Backend running (manual, cross-platform): `go run ../cmd/techstack serve --http=:5260 --dir=./pb_data`
- Frontend: the Playwright config will auto-start the dev server, or run locally with:
  - `VITE_API_URL=https://<your-techstack-host> pnpm dev`

Run tests

- Headed (default — opens a browser window):

```sh
npx playwright test --config=playwright.manual.config.ts
```

- Headless (CI-friendly):

```sh
PLAYWRIGHT_HEADLESS=true npx playwright test --config=playwright.manual.config.ts
```

Notes

- The Playwright config supports `PLAYWRIGHT_HEADLESS` to toggle headless mode.
- UI tests run serially and reuse a single browser page to avoid opening many windows.
- Use `--reporter=line` or `--reporter=dot` for compact CI output.
