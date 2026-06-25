import { mount } from "svelte";
import { defineSiguiComponents } from "@sig-ui/components";
import "./styles.css";
import App from "./App.svelte";

defineSiguiComponents({
  tagPrefix: "sg",
  featureFlags: {
    checkboxMachine: true,
    collapsibleMachine: true,
    selectMachine: true,
    sheetMachine: true,
    tabsMachine: true,
    toastMachine: true,
    tooltipMachine: true
  }
});

const target = document.getElementById("app");

if (!target) {
  throw new Error("Missing #app mount target");
}

mount(App, { target });
