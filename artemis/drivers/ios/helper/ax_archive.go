// Remote AX archive layout informed by Pinky's Apache-2.0 iOS driver.
package main

import (
	"fmt"
	"strings"

	"github.com/danielpaulus/go-ios/ios/nskeyedarchiver"
	"howett.net/plist"
)

type axRect [4]float64
type axObject struct{ pid, id uint64 }
type axRecord struct {
	kind   uint64
	object axObject
	result any
}
type axDictionary map[any]any

func decodeAXPush(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	data, _ := args[0].([]byte)
	if len(data) == 0 || len(data) > 8<<20 {
		return nil
	}
	outer, err := nskeyedarchiver.Unarchive(data)
	if err != nil || len(outer) != 1 {
		return nil
	}
	inner, ok := outer[0].([]byte)
	if !ok {
		return nil
	}
	var archive struct {
		Archiver string         `plist:"$archiver"`
		Objects  []any          `plist:"$objects"`
		Top      map[string]any `plist:"$top"`
	}
	if _, err := plist.Unmarshal(inner, &archive); err != nil || archive.Archiver != "NSKeyedArchiver" {
		return nil
	}
	reader := axArchive{objects: archive.Objects, remaining: 100_000}
	record, _ := reader.resolve(archive.Top["root"], 0).(axRecord)
	result, _ := record.result.(axDictionary)
	tree, _ := result["treeDump"].([]any)
	return tree
}

type axArchive struct {
	objects   []any
	remaining int
}

func (a *axArchive) resolve(value any, depth int) any {
	if depth > 64 || a.remaining <= 0 {
		return nil
	}
	a.remaining--
	uid, ok := value.(plist.UID)
	if !ok {
		return value
	}
	if uid == 0 || uint64(uid) >= uint64(len(a.objects)) {
		return nil
	}
	object := a.objects[uid]
	fields, ok := object.(map[string]any)
	if !ok {
		return object
	}
	classID, _ := fields["$class"].(plist.UID)
	if uint64(classID) >= uint64(len(a.objects)) {
		return nil
	}
	class, _ := a.objects[classID].(map[string]any)
	get := func(key string) any { return a.resolve(fields[key], depth+1) }
	switch class["$classname"] {
	case "NSArray", "NSMutableArray", "NSOrderedSet", "NSSet", "NSMutableSet":
		refs, _ := fields["NS.objects"].([]any)
		if len(refs) > 5000 {
			return nil
		}
		values := make([]any, 0, len(refs))
		for _, ref := range refs {
			values = append(values, a.resolve(ref, depth+1))
		}
		return values
	case "NSDictionary", "NSMutableDictionary":
		keys, _ := fields["NS.keys"].([]any)
		values, _ := fields["NS.objects"].([]any)
		if len(keys) != len(values) || len(keys) > 5000 {
			return nil
		}
		result := make(axDictionary)
		for i, ref := range keys {
			switch key := a.resolve(ref, depth+1).(type) {
			case string:
				if key == "treeDump" {
					result[key] = a.resolve(values[i], depth+1)
				}
			case uint64, int64:
				attr := int(axInteger(key))
				switch attr {
				case 21, 26, 27, 28, 30, 32, 33, 42, 61:
					result[attr] = a.resolve(values[i], depth+1)
				}
			}
		}
		return result
	case "NSString", "NSMutableString":
		return get("NS.string")
	case "NSValue":
		text, _ := get("NS.rectval").(string)
		clean := strings.NewReplacer("{", " ", "}", " ", ",", " ").Replace(text)
		var rect axRect
		if n, _ := fmt.Sscan(clean, &rect[0], &rect[1], &rect[2], &rect[3]); n == 4 {
			return rect
		}
	case "AXPTranslationObject":
		return axObject{pid: axInteger(get("pid")), id: axInteger(get("objectID"))}
	case "AXPTranslatorResponse":
		object, _ := get("associatedTranslationObject").(axObject)
		return axRecord{kind: axInteger(get("associatedRequestType")), object: object, result: get("resultData")}
	}
	return nil
}

func axInteger(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case int64:
		return uint64(n)
	}
	return 0
}
