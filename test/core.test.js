import { describe, it, expect } from "vitest";
import {
  translate,
  pinSerialize,
  pinVerify,
  isLegacyPin,
  sha256Hex,
  RU_MSG,
  RU_PREFIX,
} from "../lib/voltdrive-core.js";

describe("translate", () => {
  it("returns the input unchanged for en/uz", () => {
    expect(translate("en", "Signed in ✓")).toBe("Signed in ✓");
    expect(translate("uz", "Signed in ✓")).toBe("Signed in ✓");
  });

  it("maps static English strings to Russian", () => {
    expect(translate("ru", "Signed in ✓")).toBe("Вход выполнен ✓");
    expect(translate("ru", "Route error")).toBe("Ошибка маршрута");
    expect(translate("ru", "Sign in first")).toBe("Сначала войдите");
  });

  it("maps the hardcoded Uzbek sources", () => {
    expect(translate("ru", "Chiqdingiz")).toBe("Вы вышли");
  });

  it("handles 'prefix + value' messages", () => {
    expect(translate("ru", "Added: ali@example.com")).toBe("Добавлено: ali@example.com");
    expect(translate("ru", "Error: boom")).toBe("Ошибка: boom");
    expect(translate("ru", "Located ✓ ±12m")).toBe("Определено ✓ ±12m");
  });

  it("handles interpolated patterns", () => {
    expect(translate("ru", "✓ Reachable · ~120 km left")).toBe("✓ Хватит · ~120 км в запасе");
    expect(translate("ru", "✕ Won't reach · ~25 km short")).toBe("✕ Не хватит · ~25 км не достаёт");
    expect(translate("ru", "Saved · daily 07:30")).toBe("Сохранено · ежедневно в 07:30");
    expect(translate("ru", "Daily 07:05")).toBe("Ежедневно 07:05");
  });

  it("leaves unknown strings as-is and tolerates non-strings", () => {
    expect(translate("ru", "totally unknown xyz")).toBe("totally unknown xyz");
    expect(translate("ru", undefined)).toBe(undefined);
  });

  it("has a Russian value for every dictionary key (no empties)", () => {
    for (const [k, v] of Object.entries(RU_MSG)) expect(v, k).toBeTruthy();
    for (const [k, v] of Object.entries(RU_PREFIX)) expect(v, k).toBeTruthy();
  });
});

describe("pin hashing", () => {
  it("verifies the correct PIN and rejects wrong ones", async () => {
    const stored = await pinSerialize("uid-1", "1234");
    expect(await pinVerify("uid-1", "1234", stored)).toBe(true);
    expect(await pinVerify("uid-1", "0000", stored)).toBe(false);
    // The uid is mixed into the hash, so the same PIN under another uid fails.
    expect(await pinVerify("uid-2", "1234", stored)).toBe(false);
  });

  it("uses a fresh random salt each time", async () => {
    const a = await pinSerialize("u", "1111");
    const b = await pinSerialize("u", "1111");
    expect(a).not.toBe(b); // different salt → different stored value
    expect(await pinVerify("u", "1111", a)).toBe(true);
    expect(await pinVerify("u", "1111", b)).toBe(true);
  });

  it("stores the v2 salted format, not the legacy one", async () => {
    const stored = await pinSerialize("u", "1234");
    expect(isLegacyPin(stored)).toBe(false);
    const parsed = JSON.parse(stored);
    expect(parsed.v).toBe(2);
    expect(parsed.s).toMatch(/^[0-9a-f]{32}$/); // 16-byte salt, hex
  });

  it("still verifies and flags legacy plain-SHA256 values for upgrade", async () => {
    const legacy = await sha256Hex("u:1234");
    expect(isLegacyPin(legacy)).toBe(true);
    expect(await pinVerify("u", "1234", legacy)).toBe(true);
    expect(await pinVerify("u", "9999", legacy)).toBe(false);
  });

  it("rejects an empty stored value", async () => {
    expect(await pinVerify("u", "1234", "")).toBe(false);
    expect(await pinVerify("u", "1234", null)).toBe(false);
  });
});
