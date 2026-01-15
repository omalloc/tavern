package pathtire

import (
	"fmt"
	"regexp"
	"strings"
)

const Separator = '/'

// Node 表示 PathTrie 中的节点
type Node[K any, T any] struct {
	children map[string]*Node[K, T] // 子节点映射
	isEnd    bool                   // 是否是路径结束
	value    T                      // 节点存储的值
	wildcard *Node[K, T]            // 用于存储通配符子节点
	pattern  string                 // 存储完整的路径模式
	isRegex  bool                   // 是否是正则表达式节点
	regex    *regexp.Regexp         // 编译后的正则表达式
}

// PathTrie 表示路径前缀树
type PathTrie[K any, T any] struct {
	root *Node[K, T]
}

// NewPathTrie 创建新的 PathTrie
func NewPathTrie[K any, T any]() *PathTrie[K, T] {
	return &PathTrie[K, T]{
		root: &Node[K, T]{
			children: make(map[string]*Node[K, T]),
		},
	}
}

// Insert 插入路径和对应的值
func (t *PathTrie[K, T]) Insert(pattern string, value T) {
	current := t.root
	parts := split(pattern)

	for _, part := range parts {
		if current.children == nil {
			current.children = make(map[string]*Node[K, T])
		}

		// 处理通配符
		if part == "*" {
			if current.wildcard == nil {
				current.wildcard = &Node[K, T]{
					children: make(map[string]*Node[K, T]),
					pattern:  pattern,
				}
			}
			current = current.wildcard
			continue
		}
		// 处理正则表达式
		if strings.HasPrefix(part, "[") || strings.HasPrefix(part, ".*") {
			if current.wildcard == nil {
				regex, err := regexp.Compile(part)
				if err != nil {
					// 处理正则表达式编译错误
					fmt.Println("compile regex err ", err.Error())
					continue
				}

				current.wildcard = &Node[K, T]{
					children: make(map[string]*Node[K, T]),
					pattern:  pattern,
					isRegex:  true,
					regex:    regex,
				}
				current = current.wildcard
				continue
			}
		}

		if _, exists := current.children[part]; !exists {
			current.children[part] = &Node[K, T]{
				children: make(map[string]*Node[K, T]),
				pattern:  pattern,
			}
		}
		current = current.children[part]
	}

	current.isEnd = true
	current.value = value
}

// Search 查找路径并返回对应的值（支持通配符匹配）
func (t *PathTrie[K, T]) Search(path string) (T, bool) {
	current := t.root
	parts := split(path)

	return t.searchNode(current, parts, 0)
}

// searchNode 递归搜索节点
func (t *PathTrie[K, T]) searchNode(node *Node[K, T], parts []string, index int) (T, bool) {
	if index == len(parts) {
		if node.isEnd {
			return node.value, true
		}
		return *new(T), false
	}

	part := parts[index]

	// 1. 尝试精确匹配
	if child, exists := node.children[part]; exists {
		if value, found := t.searchNode(child, parts, index+1); found {
			return value, true
		}
	}

	// 2. 尝试通配符或正则匹配
	if node.wildcard != nil {
		if node.wildcard.isRegex {
			// 正则匹配
			if node.wildcard.regex.MatchString(part) {
				if value, found := t.searchNode(node.wildcard, parts, index+1); found {
					return value, true
				}
			}
		} else {
			// 通配符匹配
			if value, found := t.searchNode(node.wildcard, parts, index+1); found {
				return value, true
			}
		}
	}

	// 3. 若无法继续匹配，返回已命中的前缀（最长前缀匹配）
	if node.isEnd {
		return node.value, true
	}

	return *new(T), false
}

// FindByPrefix 通过前缀查找所有匹配的路径和值
func (t *PathTrie[K, T]) FindByPrefix(prefix string) map[string]T {
	results := make(map[string]T)
	current := t.root
	parts := split(prefix)

	// 定位到前缀的最后一个节点
	for _, part := range parts {
		if child, exists := current.children[part]; exists {
			current = child
		} else {
			return results // 前缀不存在
		}
	}

	// 收集该节点下的所有值
	t.collectValues(current, results)
	return results
}

// collectValues 递归收集节点下的所有值
func (t *PathTrie[K, T]) collectValues(node *Node[K, T], results map[string]T) {
	if node.isEnd {
		results[node.pattern] = node.value
	}

	// 收集普通子节点的值
	for _, child := range node.children {
		t.collectValues(child, results)
	}

	// 收集通配符节点的值
	if node.wildcard != nil {
		t.collectValues(node.wildcard, results)
	}
}

// 辅助函数：分割路径
func split(path string) []string {
	if path == "" {
		return []string{}
	}

	// 去除首尾的分隔符
	if path[0] == Separator {
		path = path[1:]
	}
	if len(path) > 0 && path[len(path)-1] == Separator {
		path = path[:len(path)-1]
	}

	// 特殊情况处理
	if path == "" {
		return []string{}
	}

	// 按分隔符分割
	var parts []string
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == Separator {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}

	return parts
}
