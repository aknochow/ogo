#!/usr/bin/env python3
"""OGO docs toolchain — build, serve, lint, search.

Usage:
    python3 scripts/ogo-docs.py              # build site
    python3 scripts/ogo-docs.py serve [port] # build and serve locally
    python3 scripts/ogo-docs.py lint         # validate frontmatter, nav, links
    python3 scripts/ogo-docs.py search QUERY # search docs by tags and descriptions
    python3 scripts/ogo-docs.py init PATH    # scaffold a new doc with frontmatter
"""

import datetime
import html as html_module
import os
import posixpath
import re
import shutil
import sys

import markdown
import tomli

DOCS_DIR = "docs"
OUT_DIR = "public"
CONFIG_FILE = "docs.toml"
_raw_base = os.environ.get("DOCS_BASE_PATH", "/ogo")
if not re.match(r'^(/[A-Za-z0-9._~:/?#@!$&()*+,;=\-]*)?$', _raw_base):
    sys.exit(f"Invalid DOCS_BASE_PATH: {_raw_base!r}")
BASE_PATH = _raw_base.rstrip("/")


def parse_frontmatter(text):
    if text.startswith("---"):
        end = text.find("---", 3)
        if end != -1:
            fm_text = text[3:end].strip()
            body = text[end + 3:].strip()
            meta = {}
            for line in fm_text.split("\n"):
                if ":" in line:
                    key, val = line.split(":", 1)
                    meta[key.strip()] = val.strip().strip('"').strip("'")
            return meta, body
    return {}, text


def extract_title(meta, body):
    if "title" in meta:
        return meta["title"]
    match = re.match(r"^#\s+(.+)", body, re.MULTILINE)
    if match:
        return match.group(1)
    return "OGO"


def md_to_slug(md_rel):
    """Convert a docs-relative .md path to its URL slug (no leading/trailing slash).

    Examples:
        index.md            -> ""
        concepts/index.md   -> "concepts"
        concepts/gateway.md -> "concepts/gateway"
    """
    parts = md_rel.replace("\\", "/").split("/")
    if parts[-1] == "index.md":
        parts = parts[:-1]
    else:
        parts[-1] = parts[-1][:-3]  # strip .md
    return "/".join(parts)


def slug_to_href(slug):
    """Convert a slug to its full site href."""
    if not slug:
        return f"{BASE_PATH}/"
    return f"{BASE_PATH}/{slug}/"


def load_nav(config_path):
    with open(config_path, "rb") as f:
        config = tomli.load(f)
    nav_raw = config.get("project", {}).get("nav", [])
    nav = []
    for section in nav_raw:
        for section_title, items in section.items():
            entries = []
            for item in items:
                for label, path in item.items():
                    slug = md_to_slug(path)
                    entries.append({"label": label, "path": slug, "file": path})
            nav.append({"title": section_title, "entries": entries})
    return nav


def build_sidebar(nav, current_file):
    html = '<nav class="pf-v6-c-nav" aria-label="Documentation">\n'
    html += '  <ul class="pf-v6-c-nav__list">\n'
    for section in nav:
        has_active = any(e["file"] == current_file for e in section["entries"])
        expanded = " pf-m-expanded" if has_active else ""
        open_attr = " open" if has_active else ""
        html += f'    <li class="pf-v6-c-nav__item pf-m-expandable{expanded}">\n'
        html += f'      <details{open_attr}>\n'
        html += f'        <summary class="pf-v6-c-nav__link">{section["title"]}\n'
        html += f'          <span class="pf-v6-c-nav__toggle-icon">▸</span>\n'
        html += f'        </summary>\n'
        html += f'        <ul class="pf-v6-c-nav__subnav">\n'
        for entry in section["entries"]:
            href = slug_to_href(entry["path"])
            active = " pf-m-current" if entry["file"] == current_file else ""
            html += f'          <li class="pf-v6-c-nav__item">\n'
            html += f'            <a class="pf-v6-c-nav__link{active}" href="{href}">{entry["label"]}</a>\n'
            html += f'          </li>\n'
        html += f'        </ul>\n'
        html += f'      </details>\n'
        html += f'    </li>\n'
    html += '  </ul>\n'
    html += '</nav>\n'
    return html


TEMPLATE = """\
<!DOCTYPE html>
<html lang="en" class="pf-v6-theme-dark">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{tab_title}</title>
  <meta name="description" content="{description}">
  <link rel="icon" href="{base_path}/favicon.svg" type="image/svg+xml">
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Red+Hat+Text:ital,wght@0,300..700;1,300..700&family=Red+Hat+Mono:wght@300..700&display=swap">
  <link rel="stylesheet" href="{base_path}/stylesheets/patternfly-base.css">
  <link rel="stylesheet" href="{base_path}/stylesheets/site.css">
</head>
<body>
  <header class="site-header">
    <a href="{base_path}/" class="site-header__brand">OGO</a>
    <span class="site-header__tagline">OpenShell Gateway Operator</span>
  </header>

  <div class="site-layout">
    <aside class="site-sidebar">
      {sidebar}
    </aside>

    <main class="site-main">
      <div class="site-content pf-v6-c-content">
        {content}
      </div>
    </main>
  </div>
{copy_script}
</body>
</html>
"""


COPY_SCRIPT = """<script>
document.querySelectorAll('pre').forEach(function(pre) {
  var btn = document.createElement('button');
  btn.className = 'copy-btn';
  btn.textContent = 'Copy';
  btn.addEventListener('click', function() {
    var code = pre.querySelector('code');
    var text = (code ? code.textContent : pre.textContent).trimEnd();
    navigator.clipboard.writeText(text).then(function() {
      btn.textContent = 'Copied!';
      setTimeout(function() { btn.textContent = 'Copy'; }, 2000);
    }).catch(function() {
      btn.textContent = 'Failed';
      setTimeout(function() { btn.textContent = 'Copy'; }, 2000);
    });
  });
  pre.style.position = 'relative';
  pre.appendChild(btn);
});
</script>"""


def rewrite_links(html, md_rel):
    """Rewrite .md hrefs to absolute site paths."""
    current_dir = posixpath.dirname(md_rel.replace("\\", "/"))

    def replace(m):
        href = m.group(1)
        frag = m.group(2) or ""
        # Resolve the href relative to the current file's directory in docs/
        if current_dir:
            resolved = posixpath.normpath(posixpath.join(current_dir, href))
        else:
            resolved = posixpath.normpath(href)
        resolved = resolved.lstrip("/")
        resolved_md = resolved + ".md"
        slug = md_to_slug(resolved_md)
        return f'href="{slug_to_href(slug)}{frag}"'

    return re.sub(r'href="([^"#:]+)\.md(#[^"]*)?"', replace, html)


def build_page(md_rel, nav, md_converter):
    """Build a single page. md_rel is path relative to DOCS_DIR."""
    with open(os.path.join(DOCS_DIR, md_rel)) as f:
        raw = f.read()

    meta, body = parse_frontmatter(raw)
    title = extract_title(meta, body)
    description = meta.get("description", "OpenShell Gateway Operator documentation")
    content = md_converter.convert(body)
    md_converter.reset()
    content = rewrite_links(content, md_rel)
    sidebar = build_sidebar(nav, md_rel)

    slug = md_to_slug(md_rel)
    tab_title = "OGO" if not slug else f"{html_module.escape(title)} · OGO"

    html = TEMPLATE.format(
        base_path=BASE_PATH,
        tab_title=tab_title,
        description=html_module.escape(description),
        sidebar=sidebar,
        content=content,
        copy_script=COPY_SCRIPT,
    )

    if not slug:
        out_path = os.path.join(OUT_DIR, "index.html")
    else:
        out_dir = os.path.join(OUT_DIR, slug)
        os.makedirs(out_dir, exist_ok=True)
        out_path = os.path.join(out_dir, "index.html")

    with open(out_path, "w") as f:
        f.write(html)

    display = "/" if not slug else f"/{slug}/"
    print(f"  + {display}")


def collect_md_files():
    """Walk docs/ and return sorted list of .md paths relative to DOCS_DIR."""
    md_files = []
    for root, dirs, files in os.walk(DOCS_DIR):
        dirs.sort()
        for fname in sorted(files):
            if fname.endswith(".md"):
                rel = os.path.relpath(os.path.join(root, fname), DOCS_DIR)
                md_files.append(rel.replace("\\", "/"))
    return md_files


def main():
    os.makedirs(OUT_DIR, exist_ok=True)

    nav = load_nav(CONFIG_FILE)

    md = markdown.Markdown(
        extensions=["tables", "fenced_code", "codehilite", "toc"],
        extension_configs={
            "codehilite": {"css_class": "highlight", "guess_lang": False},
        },
    )

    print("Build started")

    md_files = collect_md_files()
    for md_rel in md_files:
        build_page(md_rel, nav, md)

    favicon_src = os.path.join(DOCS_DIR, "favicon.svg")
    if os.path.exists(favicon_src):
        shutil.copy2(favicon_src, os.path.join(OUT_DIR, "favicon.svg"))

    styles_src = os.path.join(DOCS_DIR, "stylesheets")
    styles_dst = os.path.join(OUT_DIR, "stylesheets")
    if os.path.exists(styles_src):
        if os.path.exists(styles_dst):
            shutil.rmtree(styles_dst)
        shutil.copytree(styles_src, styles_dst)

    print(f"Build finished ({len(md_files)} pages)")


def serve(port=8000):
    import http.server
    import functools

    global BASE_PATH
    BASE_PATH = ""
    main()

    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=OUT_DIR)
    server = http.server.HTTPServer(("", port), handler)
    print(f"\nServing at http://localhost:{port}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nStopped")


VALID_TYPES = {"Guide", "Reference", "CRD Reference", "Concept", "Example", "Index"}
SKIP_FRONTMATTER = set()


def lint():
    errors = []
    warnings = []

    nav = load_nav(CONFIG_FILE)
    nav_files = set()
    for section in nav:
        for entry in section["entries"]:
            nav_files.add(entry["file"])

    md_files = collect_md_files()

    for md_rel in md_files:
        if md_rel in SKIP_FRONTMATTER:
            continue

        with open(os.path.join(DOCS_DIR, md_rel)) as f:
            raw = f.read()

        meta, body = parse_frontmatter(raw)

        if not meta:
            errors.append(f"{md_rel}: missing frontmatter")
            continue

        if not meta.get("type"):
            errors.append(f"{md_rel}: missing required field 'type'")
        elif meta["type"] not in VALID_TYPES:
            warnings.append(f"{md_rel}: type '{meta['type']}' not in {VALID_TYPES}")

        for field in ("title", "description"):
            if not meta.get(field):
                errors.append(f"{md_rel}: missing field '{field}'")

        if md_rel not in nav_files:
            warnings.append(f"{md_rel}: not in docs.toml nav")

        current_dir = posixpath.dirname(md_rel)
        for match in re.finditer(r'\[([^\]]+)\]\(([^)#]+)\.md(?:#[^)]*)?\)', body):
            href = match.group(2)
            if href.startswith("http"):
                continue
            if current_dir:
                resolved = posixpath.normpath(posixpath.join(current_dir, href))
            else:
                resolved = posixpath.normpath(href)
            resolved = resolved.lstrip("/") + ".md"
            if not os.path.exists(os.path.join(DOCS_DIR, resolved)):
                errors.append(f"{md_rel}: broken link to '{resolved}'")

    for nav_file in nav_files:
        if not os.path.exists(os.path.join(DOCS_DIR, nav_file)):
            errors.append(f"nav: '{nav_file}' in docs.toml but file missing")

    if warnings:
        for w in warnings:
            print(f"  WARN  {w}")
    if errors:
        for e in errors:
            print(f"  FAIL  {e}")
        print(f"\nLint failed: {len(errors)} error(s), {len(warnings)} warning(s)")
        sys.exit(1)
    else:
        print(f"Lint passed: {len(md_files)} docs checked, {len(warnings)} warning(s)")


def search(query):
    query_lower = query.lower()
    results = []

    for md_rel in collect_md_files():
        with open(os.path.join(DOCS_DIR, md_rel)) as f:
            raw = f.read()

        meta, _ = parse_frontmatter(raw)
        if not meta:
            continue

        title = meta.get("title", "")
        description = meta.get("description", "")
        tags = meta.get("tags", "")
        score = 0

        if query_lower in title.lower():
            score += 3
        if query_lower in description.lower():
            score += 2
        if query_lower in tags.lower():
            score += 1

        if score > 0:
            results.append((score, md_rel, meta))

    results.sort(key=lambda x: -x[0])

    if not results:
        print(f"No docs matching '{query}'")
        return

    print(f"Found {len(results)} doc(s) matching '{query}':\n")
    for score, md_rel, meta in results:
        doc_type = meta.get("type", "?")
        title = meta.get("title", md_rel)
        desc = meta.get("description", "")
        tags = meta.get("tags", "")
        print(f"  [{doc_type}] {title}")
        print(f"    {desc}")
        print(f"    tags: {tags}")
        print(f"    path: docs/{md_rel}")
        print()


def init(path):
    if os.path.exists(path):
        print(f"Error: {path} already exists")
        sys.exit(1)

    if not path.startswith("docs/"):
        print(f"Warning: {path} is outside docs/ — are you sure?")

    name = os.path.basename(path).replace(".md", "").replace("-", " ").title()

    content = f"""---
type: Guide
title: {name}
description: TODO
tags: []
---

# {name}

TODO: Write documentation.
"""
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w") as f:
        f.write(content)
    print(f"Created {path} — update frontmatter and add to docs.toml nav")


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "build"

    if cmd in ("serve", "--serve"):
        port = 8000
        for arg in sys.argv[2:]:
            if arg.isdigit():
                port = int(arg)
        serve(port)
    elif cmd == "lint":
        lint()
    elif cmd == "search":
        if len(sys.argv) < 3:
            print("Usage: ogo-docs.py search QUERY")
            sys.exit(1)
        search(" ".join(sys.argv[2:]))
    elif cmd == "init":
        if len(sys.argv) < 3:
            print("Usage: ogo-docs.py init docs/my-page.md")
            sys.exit(1)
        init(sys.argv[2])
    elif cmd == "build":
        main()
    else:
        print(f"Unknown command: {cmd}")
        print("Commands: build, serve, lint, search, init")
        sys.exit(1)
