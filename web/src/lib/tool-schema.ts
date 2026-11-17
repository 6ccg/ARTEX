// Parameter rows edit descriptions/defaults while preserving the original schema.
export type ParamRow = {
  name: string;
  type: string;
  required: boolean;
  description: string;
  defaultStr: string;
  scalar: boolean;
  parentKey?: string;
};

export function isSchemaObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function properties(schema: unknown): Record<string, unknown> {
  return isSchemaObject(schema) && isSchemaObject(schema.properties) ? schema.properties : {};
}

function requiredNames(schema: Record<string, unknown>): Set<string> {
  return new Set(
    Array.isArray(schema.required) ? schema.required.filter((name): name is string => typeof name === "string") : [],
  );
}

function row(name: string, property: Record<string, unknown>, required: Set<string>, parentKey?: string): ParamRow {
  const type = String(property.type ?? "");
  return {
    name,
    type,
    required: required.has(name),
    description: String(property.description ?? ""),
    defaultStr: property.default == null ? "" : String(property.default),
    scalar: ["string", "integer", "number", "boolean"].includes(type),
    parentKey,
  };
}

export function toRows(schema: unknown): ParamRow[] {
  if (!isSchemaObject(schema)) return [];
  const required = requiredNames(schema);
  const rows: ParamRow[] = [];
  for (const [name, property] of Object.entries(properties(schema))) {
    // Boolean schemas are valid JSON Schema, but have no editable fields.
    if (!isSchemaObject(property)) continue;
    const parameter = row(name, property, required);
    rows.push(parameter);
    if (parameter.type !== "array" || !isSchemaObject(property.items)) continue;
    const itemRequired = requiredNames(property.items);
    for (const [subName, subProperty] of Object.entries(properties(property.items))) {
      if (isSchemaObject(subProperty)) rows.push(row(subName, subProperty, itemRequired, name));
    }
  }
  return rows;
}

function coerceDefault(type: string, raw: string): unknown {
  const value = raw.trim();
  if (value === "") return undefined;
  if (type === "integer" || type === "number") {
    const number = Number(value);
    return Number.isNaN(number) ? undefined : number;
  }
  if (type === "boolean") return value === "true";
  return raw;
}

export function applyRows(schema: Record<string, unknown>, rows: ParamRow[]): Record<string, unknown> {
  const next = structuredClone(schema);
  const props = properties(next);
  for (const row of rows) {
    let property = props[row.name];
    if (row.parentKey) {
      const parent = props[row.parentKey];
      property = isSchemaObject(parent) ? properties(parent.items)[row.name] : undefined;
    }
    if (!isSchemaObject(property)) continue;
    property.description = row.description;
    if (row.scalar) {
      const value = coerceDefault(row.type, row.defaultStr);
      if (value === undefined) delete property.default;
      else property.default = value;
    }
  }
  return next;
}
