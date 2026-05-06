// Gets all non-builtin properties up the prototype chain.
function getAllProperties(object: object): Set<[object, PropertyKey]> {
  const properties = new Set<[object, PropertyKey]>();
  let current: object | null = object;

  while (current && current !== Object.prototype) {
    for (const key of Reflect.ownKeys(current)) {
      properties.add([current, key]);
    }
    current = Reflect.getPrototypeOf(current);
  }

  return properties;
}

type Pattern = string | RegExp;

interface AutoBindOptions {
  include?: Pattern[];
  exclude?: Pattern[];
}

export default function autoBind<T extends object>(
  self: T,
  { include, exclude }: AutoBindOptions = {},
): T {
  // Filter function converts key to a string for matching purposes.
  const filter = (key: PropertyKey): boolean => {
    const keyStr = typeof key === "string" ? key : key.toString();
    const match = (pattern: Pattern) =>
      typeof pattern === "string" ? keyStr === pattern : pattern.test(keyStr);

    if (include) {
      return include.some(match);
    }

    if (exclude) {
      return !exclude.some(match);
    }

    return true;
  };

  // Iterate over all properties from the prototype chain.
  const constructor = self.constructor as { prototype: object };
  const target = self as Record<PropertyKey, unknown>;

  for (const [proto, key] of getAllProperties(constructor.prototype)) {
    // Skip the constructor and any keys that don't pass the filter.
    if (key === "constructor" || !filter(key)) {
      continue;
    }

    const descriptor = Reflect.getOwnPropertyDescriptor(proto, key);
    if (descriptor && typeof descriptor.value === "function") {
      const value = target[key];
      if (typeof value === "function") {
        target[key] = value.bind(self);
      }
    }
  }

  return self;
}
