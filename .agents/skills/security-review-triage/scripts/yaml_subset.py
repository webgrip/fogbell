"""A block-style YAML reader for Kubernetes manifests, Helm values and CI workflows, with line numbers.

Stdlib only. It covers what manifests use: block mappings and sequences,
compact sequences under a key, sequence items that open a mapping, flow
collections, quoted and plain scalars, literal and folded block scalars,
anchors and tags (dropped), and multi-document streams. It does not resolve
aliases or merge keys. Malformed input raises YamlError.
"""

import re
from dataclasses import dataclass


class YamlError(Exception):
    pass


class Mapping(dict):
    def __init__(self, line: int):
        super().__init__()
        self.line = line
        self.key_lines: dict = {}


class Sequence(list):
    def __init__(self, line: int):
        super().__init__()
        self.line = line
        self.item_lines: list = []


class Text(str):
    first_line: int = 0

    @classmethod
    def at(cls, value: str, first_line: int) -> "Text":
        text = cls(value)
        text.first_line = first_line
        return text


@dataclass
class Row:
    number: int
    indent: int
    content: str


NODE_PROPERTY = re.compile(r"^(?:(?:&[^\s]+|![^\s]*)\s+)+")
BARE_PROPERTY = re.compile(r"^(?:&[^\s]+|![^\s]*)$")
INTEGER = re.compile(r"^[-+]?(?:0|[1-9][0-9]*)$")
BLOCK_HEADER = re.compile(r"^[|>][-+0-9]*$")
MAX_DEPTH = 200


def strip_comment(text: str) -> str:
    quote = ""
    depth = 0
    for index, character in enumerate(text):
        if quote:
            if character == quote:
                if quote == "'" and index + 1 < len(text) and text[index + 1] == "'":
                    continue
                if quote == '"' and index > 0 and text[index - 1] == "\\":
                    continue
                quote = ""
        elif character in "\"'" and (index == 0 or text[index - 1] in " \t[{,:-" or depth):
            quote = character
        elif character in "[{":
            depth += 1
        elif character in "]}":
            depth = max(0, depth - 1)
        elif character == "#" and (index == 0 or text[index - 1] in " \t"):
            return text[:index].rstrip()
    return text.rstrip()


def split_key(content: str):
    if not content or content[0] in "[{|>" or is_sequence_item(content):
        return None
    if content[0] in "\"'":
        quote = content[0]
        index = 1
        while index < len(content):
            if content[index] == quote:
                if quote == "'" and index + 1 < len(content) and content[index + 1] == "'":
                    index += 2
                    continue
                if quote == '"' and content[index - 1] == "\\":
                    index += 1
                    continue
                break
            index += 1
        rest = content[index + 1:].lstrip()
        if not rest.startswith(":") or (len(rest) > 1 and rest[1] not in " \t"):
            return None
        return content[1:index], rest[1:].strip()
    match = re.search(r":(?:[ \t]|$)", content)
    if not match:
        return None
    key = content[:match.start()].strip()
    if not key or key.startswith(("{", "[")):
        return None
    return key, content[match.end():].strip()


def is_sequence_item(content: str) -> bool:
    return content == "-" or content.startswith("- ")


def convert_plain(value: str):
    if value in ("true", "True", "TRUE"):
        return True
    if value in ("false", "False", "FALSE"):
        return False
    if value in ("null", "Null", "NULL", "~", ""):
        return None
    if INTEGER.match(value):
        try:
            return int(value)
        except ValueError:
            return value
    return value


def unquote(token: str) -> str:
    if token.startswith("'"):
        return token[1:-1].replace("''", "'")
    body = token[1:-1]
    return body.encode("utf-8").decode("unicode_escape", errors="replace") if "\\" in body else body


class FlowReader:
    def __init__(self, text: str, line: int):
        self.text = text
        self.position = 0
        self.line = line

    def parse(self):
        value = self.value(in_map=False)
        self.skip_space()
        if self.position != len(self.text):
            raise YamlError(f"line {self.line}: trailing text after flow collection")
        return value

    def skip_space(self):
        while self.position < len(self.text) and self.text[self.position] in " \t\n":
            self.position += 1

    def value(self, in_map: bool):
        self.skip_space()
        if self.position >= len(self.text):
            return None
        character = self.text[self.position]
        if character == "[":
            return self.sequence()
        if character == "{":
            return self.mapping()
        if character in "\"'":
            return self.quoted()
        return self.plain(in_map)

    def sequence(self):
        result = Sequence(self.line)
        self.position += 1
        while True:
            self.skip_space()
            if self.position >= len(self.text):
                raise YamlError(f"line {self.line}: unclosed flow sequence")
            if self.text[self.position] == "]":
                self.position += 1
                return result
            result.append(self.value(in_map=False))
            result.item_lines.append(self.line)
            self.skip_space()
            if self.position < len(self.text) and self.text[self.position] == ",":
                self.position += 1

    def mapping(self):
        result = Mapping(self.line)
        self.position += 1
        while True:
            self.skip_space()
            if self.position >= len(self.text):
                raise YamlError(f"line {self.line}: unclosed flow mapping")
            if self.text[self.position] == "}":
                self.position += 1
                return result
            key = self.value(in_map=True)
            self.skip_space()
            value = None
            if self.position < len(self.text) and self.text[self.position] == ":":
                self.position += 1
                value = self.value(in_map=True)
            result[str(key)] = value
            result.key_lines[str(key)] = self.line
            self.skip_space()
            if self.position < len(self.text) and self.text[self.position] == ",":
                self.position += 1

    def quoted(self):
        quote = self.text[self.position]
        end = self.position + 1
        while end < len(self.text):
            if self.text[end] == quote:
                if quote == "'" and end + 1 < len(self.text) and self.text[end + 1] == "'":
                    end += 2
                    continue
                if quote == '"' and self.text[end - 1] == "\\":
                    end += 1
                    continue
                break
            end += 1
        token = self.text[self.position:end + 1]
        self.position = end + 1
        return unquote(token)

    def plain(self, in_map: bool):
        start = self.position
        while self.position < len(self.text):
            character = self.text[self.position]
            if character in ",]}":
                break
            if character == ":" and in_map:
                following = self.text[self.position + 1:self.position + 2]
                if following in ("", " ", ",", "}", "\n"):
                    break
            self.position += 1
        return convert_plain(self.text[start:self.position].strip())


class Reader:
    def __init__(self, lines: list[tuple[int, str]]):
        self.lines = lines
        self.index = 0
        self.virtual: dict[int, Row] = {}
        self.depth = 0

    def row(self, index: int):
        if index in self.virtual:
            return self.virtual[index]
        number, text = self.lines[index]
        if "\t" in text[:len(text) - len(text.lstrip())]:
            raise YamlError(f"line {number}: tab indentation")
        content = strip_comment(text).strip()
        if not content:
            return None
        return Row(number, len(text) - len(text.lstrip(" ")), content)

    def peek(self):
        while self.index < len(self.lines):
            row = self.row(self.index)
            if row is not None:
                return row
            self.index += 1
        return None

    def consume(self):
        self.virtual.pop(self.index, None)
        self.index += 1

    def node(self, indent: int):
        self.depth += 1
        if self.depth > MAX_DEPTH:
            raise YamlError("nesting too deep")
        try:
            row = self.peek()
            if row is None or row.indent < indent:
                return None
            if is_sequence_item(row.content):
                return self.sequence(row.indent)
            if split_key(row.content) is not None:
                return self.mapping(row.indent)
            self.consume()
            return self.value(row.content, row.indent - 1, row.number, key_owner=False)
        finally:
            self.depth -= 1

    def mapping(self, indent: int) -> Mapping:
        first = self.peek()
        result = Mapping(first.number)
        while True:
            row = self.peek()
            if row is None or row.indent < indent:
                return result
            if row.indent > indent:
                raise YamlError(f"line {row.number}: unexpected indentation")
            if is_sequence_item(row.content):
                return result
            parts = split_key(row.content)
            if parts is None:
                raise YamlError(f"line {row.number}: expected a key")
            key, rest = parts
            self.consume()
            result.key_lines[key] = row.number
            result[key] = self.value(rest, indent, row.number, key_owner=True)

    def sequence(self, indent: int) -> Sequence:
        first = self.peek()
        result = Sequence(first.number)
        while True:
            row = self.peek()
            if row is None or row.indent != indent or not is_sequence_item(row.content):
                if row is not None and row.indent > indent:
                    raise YamlError(f"line {row.number}: unexpected indentation")
                return result
            remainder = row.content[1:]
            item = remainder.lstrip()
            result.item_lines.append(row.number)
            if not item:
                self.consume()
                result.append(self.value("", indent, row.number, key_owner=False))
                continue
            item_indent = indent + 1 + len(remainder) - len(item)
            stripped = NODE_PROPERTY.sub("", item)
            if is_sequence_item(item) or (split_key(stripped) is not None and stripped[:1] not in "[{\"'") or (stripped[:1] in "\"'" and split_key(stripped) is not None):
                self.virtual[self.index] = Row(row.number, item_indent, stripped)
                result.append(self.node(item_indent))
            else:
                self.consume()
                result.append(self.value(item, indent, row.number, key_owner=False))

    def value(self, rest: str, indent: int, number: int, key_owner: bool):
        rest = NODE_PROPERTY.sub("", rest + " ").strip() if rest else rest
        if BARE_PROPERTY.match(rest or "-"):
            rest = ""
        if rest == "":
            row = self.peek()
            if row is None:
                return None
            if row.indent > indent:
                return self.node(row.indent)
            if key_owner and row.indent == indent and is_sequence_item(row.content):
                return self.sequence(indent)
            return None
        if BLOCK_HEADER.match(rest):
            return self.block_scalar(indent, rest[0] == ">")
        if rest[0] in "[{":
            return self.flow(rest, number)
        if rest[0] in "\"'":
            return self.quoted(rest, number)
        if rest.startswith("*"):
            return rest
        return convert_plain(self.continued(rest, indent))

    def continued(self, rest: str, indent: int) -> str:
        parts = [rest]
        while True:
            row = self.peek()
            if row is None or row.indent <= indent or self.index in self.virtual:
                return " ".join(parts)
            parts.append(row.content)
            self.consume()

    def flow(self, rest: str, number: int):
        text = rest
        while not self.balanced(text):
            if self.index >= len(self.lines):
                raise YamlError(f"line {number}: unclosed flow collection")
            text += "\n" + strip_comment(self.lines[self.index][1]).strip()
            self.consume()
        return FlowReader(text, number).parse()

    @staticmethod
    def balanced(text: str) -> bool:
        depth = 0
        quote = ""
        for index, character in enumerate(text):
            if quote:
                if character == quote and not (quote == '"' and text[index - 1] == "\\"):
                    quote = ""
            elif character in "\"'":
                quote = character
            elif character in "[{":
                depth += 1
            elif character in "]}":
                depth -= 1
        return depth <= 0

    def quoted(self, rest: str, number: int):
        quote = rest[0]
        text = rest
        while not self.closed(text, quote):
            if self.index >= len(self.lines):
                raise YamlError(f"line {number}: unclosed quoted scalar")
            text += " " + self.lines[self.index][1].strip()
            self.consume()
        end = self.closing_index(text, quote)
        return unquote(text[:end + 1])

    @staticmethod
    def closing_index(text: str, quote: str) -> int:
        index = 1
        while index < len(text):
            if text[index] == quote:
                if quote == "'" and index + 1 < len(text) and text[index + 1] == "'":
                    index += 2
                    continue
                if quote == '"' and text[index - 1] == "\\":
                    index += 1
                    continue
                return index
            index += 1
        return -1

    def closed(self, text: str, quote: str) -> bool:
        return self.closing_index(text, quote) != -1

    def block_scalar(self, indent: int, folded: bool) -> Text:
        collected: list[str] = []
        block_indent = None
        first_line = 0
        while self.index < len(self.lines):
            number, text = self.lines[self.index]
            if not text.strip():
                collected.append("")
                self.index += 1
                continue
            current = len(text) - len(text.lstrip(" "))
            if block_indent is None:
                if current <= indent:
                    break
                block_indent = current
                first_line = number
            if current < block_indent:
                break
            collected.append(text[block_indent:])
            self.index += 1
        while collected and collected[-1] == "":
            collected.pop()
        joiner = " " if folded else "\n"
        return Text.at(joiner.join(collected), first_line)


def documents(text: str):
    current: list[tuple[int, str]] = []
    chunks: list[list[tuple[int, str]]] = []
    for number, line in enumerate(text.splitlines(), start=1):
        if line.startswith("---") and (len(line) == 3 or line[3] in " \t"):
            chunks.append(current)
            current = []
            remainder = line[3:].strip()
            if remainder and not remainder.startswith("#"):
                current.append((number, " " * 4 + remainder))
            continue
        if line.startswith("...") and line.strip() == "...":
            chunks.append(current)
            current = []
            continue
        if line.startswith("%"):
            continue
        current.append((number, line))
    chunks.append(current)
    for chunk in chunks:
        reader = Reader(chunk)
        row = reader.peek()
        if row is None:
            continue
        value = reader.node(row.indent)
        leftover = reader.peek()
        if leftover is not None:
            raise YamlError(f"line {leftover.number}: content after the document's root node")
        yield value
