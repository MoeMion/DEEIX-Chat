import assert from "node:assert/strict";
import { test } from "node:test";
import { apiRequest, resolveApiBaseURL } from "./http-client";

test("API requests use the page origin unless explicitly configured", async (t) => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
  const originalBaseURL = process.env.NEXT_PUBLIC_API_BASE_URL;
  t.after(() => {
    if (originalWindow) {
      Object.defineProperty(globalThis, "window", originalWindow);
    } else {
      Reflect.deleteProperty(globalThis, "window");
    }
    if (originalBaseURL === undefined) {
      delete process.env.NEXT_PUBLIC_API_BASE_URL;
    } else {
      process.env.NEXT_PUBLIC_API_BASE_URL = originalBaseURL;
    }
  });

  delete process.env.NEXT_PUBLIC_API_BASE_URL;
  Reflect.deleteProperty(globalThis, "window");
  assert.equal(resolveApiBaseURL(), "");

  for (const origin of [
    "http://127.0.0.1:8081",
    "http://localhost:8081",
    "http://[::1]:8081",
    "http://localhost:3000",
    "https://localhost:8443",
    "http://127.0.0.1:8080",
    "https://chat.example.com",
  ]) {
    Object.defineProperty(globalThis, "window", {
      configurable: true,
      value: { location: new URL(origin) },
    });
    assert.equal(resolveApiBaseURL(), origin);
  }

  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { location: new URL("http://127.0.0.1:8081") },
  });
  process.env.NEXT_PUBLIC_API_BASE_URL = "   ";
  const fetchMock = t.mock.method(globalThis, "fetch", async () =>
    Response.json({ errorMsg: "", data: { ok: true } }),
  );
  assert.deepEqual(await apiRequest("/api/v1/auth/login", {
    method: "POST",
    body: { username: "test", password: "test-password" },
  }), { ok: true });
  assert.equal(fetchMock.mock.calls[0].arguments[0], "http://127.0.0.1:8081/api/v1/auth/login");
  assert.equal(fetchMock.mock.calls[0].arguments[1]?.credentials, "include");

  process.env.NEXT_PUBLIC_API_BASE_URL = " http://127.0.0.1:8080/// ";
  assert.equal(resolveApiBaseURL(), "http://127.0.0.1:8080");
  Reflect.deleteProperty(globalThis, "window");
  assert.equal(resolveApiBaseURL(), "http://127.0.0.1:8080");
});
