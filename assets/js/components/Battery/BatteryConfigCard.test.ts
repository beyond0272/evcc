import { shallowMount } from "@vue/test-utils";
import { describe, expect, it } from "vite-plus/test";
import BatteryConfigCard from "./BatteryConfigCard.vue";

describe("PV control availability", () => {
  it("explains blocked control while preserving the configured threshold", () => {
    const wrapper = shallowMount(BatteryConfigCard, {
      props: { batteryPvControlUnavailable: true, batteryPvStartPower: 750 },
      global: {
        renderStubDefaultSlot: true,
        mocks: { $t: (key: string) => key, $i18n: { locale: "de-DE" } },
      },
    });
    expect(wrapper.get('[role="status"]').text()).toBe("battery.config.pvControlUnavailable");
    expect((wrapper.get("#batteryPVStartPower").element as HTMLInputElement).value).toBe("750");
    wrapper.unmount();
  });
});
