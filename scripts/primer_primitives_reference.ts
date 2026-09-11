import {mkdir, readdir, readFile, rm, writeFile} from "node:fs/promises";
import path from "node:path";

type PrimerPrimitiveSource = {
  sourcePath: string;
  relativeSource: string;
};

type PrimerPrimitiveReference = {
  title: string;
  outputPath: string;
  sources: PrimerPrimitiveSource[];
};

async function listCssSources(root: string): Promise<string[]> {
  const entries = await readdir(root, {withFileTypes: true});
  const files = await Promise.all(
    entries.map(async entry => {
      const fullPath = path.join(root, entry.name);

      if (entry.isDirectory()) {
        return listCssSources(fullPath);
      }

      if (entry.isFile() && entry.name.endsWith(".css") && entry.name !== "primitives.css") {
        return [fullPath];
      }

      return [];
    }),
  );

  return files.flat().sort((left, right) => left.localeCompare(right));
}

function primitiveSourcePath(source: PrimerPrimitiveSource): string {
  return `@primer/primitives/dist/css/${source.relativeSource.split(path.sep).join("/")}`;
}

async function renderReference(reference: PrimerPrimitiveReference): Promise<string> {
  const sections = await Promise.all(
    reference.sources.map(async source => {
      const css = (await readFile(source.sourcePath, "utf8")).trimEnd();

      return `/* Source: ${primitiveSourcePath(source)} */\n\n${css}`;
    }),
  );

  return `/* ${reference.title} */\n\n${sections.join("\n\n")}\n`;
}

function referenceGroups(sources: PrimerPrimitiveSource[]): PrimerPrimitiveReference[] {
  const byRelativeSource = new Map(sources.map(source => [source.relativeSource.split(path.sep).join("/"), source]));
  const source = (relativeSource: string) => byRelativeSource.get(relativeSource);
  const pick = (relativeSources: string[]) => relativeSources
    .map(relativeSource => source(relativeSource))
    .filter((item): item is PrimerPrimitiveSource => item !== undefined);

  return [
    {
      title: "Motion",
      outputPath: "motion.css",
      sources: pick(["base/motion/motion.css", "functional/motion/motion.css"]),
    },
    {
      title: "Size",
      outputPath: "size.css",
      sources: pick([
        "base/size/size.css",
        "base/size/z-index.css",
        "functional/size/border.css",
        "functional/size/breakpoints.css",
        "functional/size/radius.css",
        "functional/size/size-coarse.css",
        "functional/size/size-fine.css",
        "functional/size/size.css",
        "functional/size/viewport.css",
        "functional/size/z-index.css",
        "functional/spacing/space.css",
      ]),
    },
    {
      title: "Typography",
      outputPath: "typography.css",
      sources: pick(["base/typography/typography.css", "functional/typography/typography.css"]),
    },
    {
      title: "Dark Theme",
      outputPath: "theme-dark.css",
      sources: pick(["functional/themes/dark.css"]),
    },
    {
      title: "Light Theme",
      outputPath: "theme-light.css",
      sources: pick(["functional/themes/light.css"]),
    },
  ].filter(reference => reference.sources.length > 0);
}

export async function generatePrimerPrimitivesReference(options?: {
  sourceRoot?: string;
  outputRoot?: string;
}): Promise<string[]> {
  const sourceRoot =
    options?.sourceRoot ?? path.join(process.cwd(), "node_modules", "@primer", "primitives", "dist", "css");
  const outputRoot = options?.outputRoot ?? path.join(process.cwd(), "docs", "reference", "primer-primitives-css");
  const sources = (await listCssSources(sourceRoot)).map(sourcePath => {
    const relativeSource = path.relative(sourceRoot, sourcePath);

    return {
      sourcePath,
      relativeSource,
    };
  });
  const references = referenceGroups(sources);
  const writtenFiles: string[] = [];

  await rm(outputRoot, {recursive: true, force: true});
  await mkdir(outputRoot, {recursive: true});

  for (const reference of references) {
    const outputPath = path.join(outputRoot, reference.outputPath);
    const css = await renderReference(reference);

    await mkdir(path.dirname(outputPath), {recursive: true});
    await writeFile(outputPath, css, "utf8");
    writtenFiles.push(outputPath);
  }

  return writtenFiles;
}

if (import.meta.main) {
  const files = await generatePrimerPrimitivesReference();
  console.log(`Generated ${files.length} Primer primitive CSS reference files.`);
}
