import { select } from "@inquirer/prompts";
import { TEMPLATES, type TemplateName } from "./templates.js";

export async function promptTemplate(): Promise<TemplateName> {
  return select({
    message: "Select a template:",
    choices: Object.entries(TEMPLATES).map(([name, { description }]) => ({
      name: `${name} - ${description}`,
      value: name as TemplateName,
    })),
  });
}
