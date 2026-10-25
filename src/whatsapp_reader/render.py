from datetime import date

from .summarise import GroupDigest


def render(day: date, digests: dict[str, GroupDigest]) -> str:
    out = [f"# Group digest: {day:%a %d %b %Y}", ""]
    quiet = []
    for group, d in digests.items():
        if d.nothing_important:
            quiet.append(group)
            continue
        out.append(f"## {group}")
        for e in d.events:
            details = " · ".join(x for x in (e.when, e.where) if x)
            out.append(f"- 📅 **{e.title}**" + (f" ({details})" if details else ""))
        for dl in d.deadlines:
            out.append(f"- ⏰ **{dl.what}**" + (f" due {dl.due}" if dl.due else ""))
        for a in d.announcements:
            out.append(f"- 📢 {a}")
        out.append("")
    if quiet:
        out.append(f"_Nothing important in: {', '.join(quiet)}_")
    return "\n".join(out).rstrip() + "\n"
