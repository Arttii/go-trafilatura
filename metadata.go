package trafilatura

import (
	"encoding/json"
	nurl "net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-shiori/dom"
	"golang.org/x/net/html"
)

var (
	rxCommaSeparator  = regexp.MustCompile(`\s*[,;]\s*`)
	rxTitleCleaner    = regexp.MustCompile(`(?i)(.+)?\s+[-|]\s+.*$`)
	rxJsonSymbol      = regexp.MustCompile(`[{\\}]`)
	rxNameJson        = regexp.MustCompile(`(?i)"name?\\?": ?\\?"([^"\\]+)`)
	rxAuthorCleaner1  = regexp.MustCompile(`(?i)^([a-zäöüß]+(ed|t))?\s?(by|von)\s`)
	rxAuthorCleaner2  = regexp.MustCompile(`(?i)\d.+?$`)
	rxAuthorCleaner3  = regexp.MustCompile(`(?i)[^\w]+$|( am| on)`)
	rxUrlCheck        = regexp.MustCompile(`(?i)https?://|/`)
	rxDomainFinder    = regexp.MustCompile(`(?i)https?://[^/]+`)
	rxSitenameFinder1 = regexp.MustCompile(`(?i)^.*?[-|]\s+(.*)$`)
	rxSitenameFinder2 = regexp.MustCompile(`(?i)https?://(?:www\.|w[0-9]+\.)?([^/]+)`)

	metaNameAuthor      = []string{"author", "byl", "dc.creator", "dcterms.creator", "sailthru.author"} // twitter:creator
	metaNameTitle       = []string{"title", "dc.title", "dcterms.title", "fb_title", "sailthru.title", "twitter:title"}
	metaNameDescription = []string{"description", "dc.description", "dcterms.description", "dc:description", "sailthru.description", "twitter:description"}
	metaNamePublisher   = []string{"copyright", "dc.publisher", "dcterms.publisher", "publisher"}
)

type Metadata struct {
	Title       string
	Author      string
	URL         string
	Hostname    string
	Description string
	Sitename    string
	Date        string
	Categories  []string
	Tags        []string
}

func extractMetadata(doc *html.Node, defaultURL *nurl.URL) Metadata {
	// Extract metadata from <meta> tags
	metadata := processMetaTags(doc)

	// Extract metadata from JSON-LD and override
	metadata = extractJsonLd(doc, metadata)

	// Try extracting from DOM element using selectors
	// Title
	if metadata.Title == "" {
		metadata.Title = extractDomTitle(doc)
	}

	// Author
	if metadata.Author == "" {
		metadata.Author = extractDomAuthor(doc)
	}

	// URL
	if metadata.URL == "" {
		metadata.URL = extractDomURL(doc, defaultURL)
	}

	// Hostname
	if metadata.URL != "" {
		metadata.Hostname = extractDomainURL(metadata.URL)
	}

	// TODO: Publish date (need to port htmldate) :(

	// Sitename
	if metadata.Sitename == "" {
		metadata.Sitename = extractDomSitename(doc)
	}

	if metadata.Sitename != "" {
		// Scrap Twitter ID
		if strings.HasPrefix(metadata.Sitename, "@") {
			metadata.Sitename = strings.TrimPrefix(metadata.Sitename, "@")
		}

		// Capitalize
		firstRune := getRune(metadata.Sitename, 0)
		if !strings.Contains(metadata.Sitename, ".") && !unicode.IsUpper(firstRune) {
			metadata.Sitename = strings.Title(metadata.Sitename)
		}
	} else if metadata.URL != "" {
		matches := rxSitenameFinder2.FindStringSubmatch(metadata.URL)
		if len(matches) > 0 {
			metadata.Sitename = matches[1]
		}
	}

	// Categories
	if len(metadata.Categories) == 0 {
		metadata.Categories = extractDomCategories(doc)
	}

	if len(metadata.Categories) != 0 {
		metadata.Categories = cleanCatTags(metadata.Categories)
	}

	// Tags
	if len(metadata.Tags) == 0 {
		metadata.Tags = extractDomTags(doc)
	}

	if len(metadata.Tags) != 0 {
		metadata.Tags = cleanCatTags(metadata.Tags)
	}

	return metadata
}

// processMetaTags search meta tags for relevant information
func processMetaTags(doc *html.Node) Metadata {
	// Bootstrap metadata from OpenGraph tags
	metadata := extractOpenGraphMeta(doc)

	// If all OpenGraph metadata have been assigned, we can return it
	if metadata.Title != "" && metadata.Author != "" && metadata.URL != "" &&
		metadata.Description != "" && metadata.Sitename != "" {
		return metadata
	}

	// Scan all <meta> nodes that has attribute "content"
	var tmpSitename string
	for _, node := range dom.QuerySelectorAll(doc, "meta[content]") {
		// Make sure content is not empty
		content := dom.GetAttribute(node, "content")
		content = strNormalize(content)
		if content == "" {
			continue
		}

		// Handle property attribute
		property := dom.GetAttribute(node, "property")
		property = strNormalize(property)

		if property != "" {
			switch {
			case strings.HasPrefix(property, "og:"):
				// We already handle OpenGraph before
			case property == "article:tag":
				metadata.Tags = append(metadata.Tags, content)
			case strIn(property, "author", "article:author"):
				metadata.Author = strOr(metadata.Author, content)
			}
			continue
		}

		// Handle name attribute
		name := dom.GetAttribute(node, "name")
		name = strings.ToLower(name)
		name = strNormalize(name)

		if name != "" {
			if strIn(name, metaNameAuthor...) {
				metadata.Author = strOr(metadata.Author, content)
			} else if strIn(name, metaNameTitle...) {
				metadata.Title = strOr(metadata.Title, content)
			} else if strIn(name, metaNameDescription...) {
				metadata.Description = strOr(metadata.Description, content)
			} else if strIn(name, metaNamePublisher...) {
				metadata.Sitename = strOr(metadata.Sitename, content)
			} else if strIn(name, "twitter:site", "application-name") || strings.Contains(name, "twitter:app:name") {
				tmpSitename = content
			} else if name == "twitter:url" {
				if isAbs, _ := isAbsoluteURL(content); metadata.URL == "" && isAbs {
					metadata.URL = content
				}
			} else if name == "keywords" { // "page-topic"
				metadata.Tags = append(metadata.Tags, content)
			}
			continue
		}

		// Handle itemprop attribute
		itemprop := dom.GetAttribute(node, "itemprop")
		itemprop = strNormalize(itemprop)

		if itemprop != "" {
			switch itemprop {
			case "author":
				metadata.Author = strOr(metadata.Author, content)
			case "description":
				metadata.Description = strOr(metadata.Description, content)
			case "headline":
				metadata.Title = strOr(metadata.Title, content)
			}
			continue
		}
	}

	// Use temporary site name if necessary
	if metadata.Sitename == "" && tmpSitename != "" {
		metadata.Sitename = tmpSitename
	}

	// Clean up author
	metadata.Author = validateMetadataAuthor(metadata.Author)
	return metadata
}

// extractOpenGraphMeta search meta tags following the OpenGraph guidelines (https://ogp.me/)
func extractOpenGraphMeta(doc *html.Node) Metadata {
	var metadata Metadata

	// Scan all <meta> nodes whose property starts with "og:"
	for _, node := range dom.QuerySelectorAll(doc, `meta[property^="og:"]`) {
		// Get property name
		propName := dom.GetAttribute(node, "property")
		propName = strNormalize(propName)

		// Make sure node has content attribute
		content := dom.GetAttribute(node, "content")
		content = strNormalize(content)
		if content == "" {
			continue
		}

		// Fill metadata
		switch propName {
		case "og:site_name":
			metadata.Sitename = content
		case "og:title":
			metadata.Title = content
		case "og:description":
			metadata.Description = content
		case "og:author", "og:article:author":
			metadata.Author = content
		case "og:url":
			if isAbs, _ := isAbsoluteURL(content); isAbs {
				metadata.URL = content
			}
		}
	}

	return metadata
}

// extractJsonLd search metadata from JSON+LD data following the Schema.org guidelines
// (https://schema.org). Here we don't really care about error here, so if parse failed
// we just return the original metadata.
func extractJsonLd(doc *html.Node, originalMetadata Metadata) Metadata {
	// Find all script nodes that contain JSON+Ld schema
	scriptNodes1 := dom.QuerySelectorAll(doc, `script[type="application/ld+json"]`)
	scriptNodes2 := dom.QuerySelectorAll(doc, `script[type="application/settings+json"]`)
	scriptNodes := append(scriptNodes1, scriptNodes2...)

	// Process each script node
	var metadata Metadata
	for _, script := range scriptNodes {
		// Get the json text inside the script
		jsonLdText := dom.TextContent(script)
		jsonLdText = strings.TrimSpace(jsonLdText)
		if jsonLdText == "" {
			continue
		}

		// Decode JSON text, assuming it is an object
		data := map[string]interface{}{}
		err := json.Unmarshal([]byte(jsonLdText), &data)
		if err != nil {
			continue
		}

		// Find articles and persons inside JSON+LD recursively
		persons := make([]map[string]interface{}, 0)
		articles := make([]map[string]interface{}, 0)

		var findImportantObjects func(obj map[string]interface{})
		findImportantObjects = func(obj map[string]interface{}) {
			// First check if this object type matches with our need.
			if objType, hasType := obj["@type"]; hasType {
				if strObjType, isString := objType.(string); isString {
					isPerson := strObjType == "Person"
					isArticle := strings.Contains(strObjType, "Article") ||
						strObjType == "SocialMediaPosting" ||
						strObjType == "Report"

					switch {
					case isArticle:
						articles = append(articles, obj)
						return

					case isPerson:
						persons = append(persons, obj)
						return
					}
				}
			}

			// If not, look in its children
			for _, value := range obj {
				switch v := value.(type) {
				case map[string]interface{}:
					findImportantObjects(v)

				case []interface{}:
					for _, item := range v {
						itemObject, isObject := item.(map[string]interface{})
						if isObject {
							findImportantObjects(itemObject)
						}
					}
				}
			}
		}

		findImportantObjects(data)

		// Extract metadata from each article
		for _, article := range articles {
			if metadata.Author == "" {
				// For author, if taken from schema, we only want it from schema with type "Person"
				metadata.Author = extractJsonArticleThingName(article, "author", "Person")
				metadata.Author = validateMetadataAuthor(metadata.Author)
			}

			if metadata.Sitename == "" {
				metadata.Sitename = extractJsonArticleThingName(article, "publisher")
			}

			if len(metadata.Categories) == 0 {
				if section, exist := article["articleSection"]; exist {
					category := extractJsonString(section)
					metadata.Categories = append(metadata.Categories, category)
				}
			}

			if metadata.Title == "" {
				if name, exist := article["name"]; exist {
					metadata.Title = extractJsonString(name)
				}
			}

			// If title is empty or only consist of one word, try to look in headline
			if metadata.Title == "" || strWordCount(metadata.Title) == 1 {
				for key, value := range article {
					if !strings.Contains(strings.ToLower(key), "headline") {
						continue
					}

					title := extractJsonString(value)
					if title == "" || strings.Contains(title, "...") {
						continue
					}

					metadata.Title = title
					break
				}
			}
		}

		// If author not found, look in persons
		if metadata.Author == "" {
			names := []string{}
			for _, person := range persons {
				personName := extractJsonThingName(person)
				personName = validateMetadataAuthor(personName)
				if personName != "" {
					names = append(names, personName)
				}
			}

			if len(names) > 0 {
				metadata.Author = strings.Join(names, "; ")
			}
		}

		// Stop if all metadata found
		if metadata.Author != "" && metadata.Sitename != "" &&
			len(metadata.Categories) != 0 && metadata.Title != "" {
			break
		}
	}

	// If available, override author and categories in original metadata
	originalMetadata.Author = strOr(metadata.Author, originalMetadata.Author)
	if len(metadata.Categories) > 0 {
		originalMetadata.Categories = metadata.Categories
	}

	// If the new sitename exist and longer, override the original
	if utf8.RuneCountInString(metadata.Sitename) > utf8.RuneCountInString(originalMetadata.Sitename) {
		originalMetadata.Sitename = metadata.Sitename
	}

	// The new title is only used if original metadata doesn't have any title
	if originalMetadata.Title == "" {
		originalMetadata.Title = metadata.Title
	}

	return originalMetadata
}

func extractJsonArticleThingName(article map[string]interface{}, key string, allowedTypes ...string) string {
	// Fetch value from the key
	value, exist := article[key]
	if !exist {
		return ""
	}

	return extractJsonThingName(value, allowedTypes...)
}

func extractJsonThingName(iface interface{}, allowedTypes ...string) string {
	// Decode the value of interface
	switch val := iface.(type) {
	case string:
		// There are some case where the string contains an unescaped
		// JSON, so try to handle it here
		if rxJsonSymbol.MatchString(val) {
			matches := rxNameJson.FindStringSubmatch(val)
			if len(matches) == 0 {
				return ""
			}
			val = matches[1]
		}

		// Clean up the string
		return strNormalize(val)

	case map[string]interface{}:
		// If it's object, make sure its type allowed
		if len(allowedTypes) > 0 {
			if objType, hasType := val["@type"]; hasType {
				if strObjType, isString := objType.(string); isString {
					if !strIn(strObjType, allowedTypes...) {
						return ""
					}
				}
			}
		}

		// Return its name
		if iName, exist := val["name"]; exist {
			return extractJsonString(iName)
		}

	case []interface{}:
		// If it's array, merge names into one
		names := []string{}
		for _, entry := range val {
			switch entryVal := entry.(type) {
			case string:
				entryVal = strNormalize(entryVal)
				names = append(names, entryVal)

			case map[string]interface{}:
				if iName, exist := entryVal["name"]; exist {
					if name := extractJsonString(iName); name != "" {
						names = append(names, name)
					}
				}
			}
		}

		if len(names) > 0 {
			return strings.Join(names, "; ")
		}
	}

	return ""
}

func extractJsonString(iface interface{}) string {
	if s, isString := iface.(string); isString {
		return strNormalize(s)
	}

	return ""
}

// extractDomTitle returns the document title from DOM elements.
func extractDomTitle(doc *html.Node) string {
	// If there are only one H1, use it as title
	h1Nodes := dom.QuerySelectorAll(doc, "h1")
	if len(h1Nodes) == 1 {
		title := dom.TextContent(h1Nodes[0])
		return strNormalize(title)
	}

	// Look for title using several CSS selectors
	title := extractDomMetaSelectors(doc, 200, metaTitleSelectors...)
	if title != "" {
		return title
	}

	// Look in <title> tag
	titleNode := dom.QuerySelector(doc, "head > title")
	if titleNode != nil {
		title := dom.TextContent(titleNode)
		title = strNormalize(title)

		matches := rxTitleCleaner.FindStringSubmatch(title)
		if len(matches) > 0 {
			title = matches[1]
		}

		return title
	}

	// If still not found, just use the first H1 as it is
	if len(h1Nodes) > 0 {
		title := dom.TextContent(h1Nodes[0])
		return strings.TrimSpace(title)
	}

	// If STILL not found, use the first H2 as it is
	h2Node := dom.QuerySelector(doc, "h2")
	if h2Node != nil {
		title := dom.TextContent(h2Node)
		return strings.TrimSpace(title)
	}

	return ""
}

// extractDomTitle returns the document author from DOM elements.
func extractDomAuthor(doc *html.Node) string {
	author := extractDomMetaSelectors(doc, 75, metaAuthorSelectors...)
	if author != "" {
		author = rxAuthorCleaner1.ReplaceAllString(author, "")
		author = rxAuthorCleaner2.ReplaceAllString(author, "")
		author = rxAuthorCleaner3.ReplaceAllString(author, "")
		author = strings.Title(author)
		return author
	}

	return ""
}

// extractDomURL extracts the document URL from the canonical <link>.
func extractDomURL(doc *html.Node, defaultURL *nurl.URL) string {
	var url string

	// Try canonical link first
	linkNode := dom.QuerySelector(doc, `head link[rel="canonical"]`)
	if linkNode != nil {
		href := dom.GetAttribute(linkNode, "href")
		href = strNormalize(href)
		if href != "" && rxUrlCheck.MatchString(href) {
			url = href
		}
	} else {
		// Now try default language link
		linkNodes := dom.QuerySelectorAll(doc, `head link[rel="alternate"]`)
		for _, node := range linkNodes {
			hreflang := dom.GetAttribute(node, "hreflang")
			if hreflang == "x-default" {
				href := dom.GetAttribute(node, "href")
				href = strNormalize(href)
				if href != "" && rxUrlCheck.MatchString(href) {
					url = href
				}
			}
		}
	}

	// Add domain name if it's missing
	if url != "" && strings.HasPrefix(url, "/") {
		for _, node := range dom.QuerySelectorAll(doc, "head meta[content]") {
			nodeName := strNormalize(dom.GetAttribute(node, "name"))
			nodeProperty := strNormalize(dom.GetAttribute(node, "property"))

			attrType := strOr(nodeName, nodeProperty)
			if attrType == "" {
				continue
			}

			if strings.HasPrefix(attrType, "og:") || strings.HasPrefix(attrType, "twitter:") {
				nodeContent := strNormalize(dom.GetAttribute(node, "content"))
				domainMatches := rxDomainFinder.FindStringSubmatch(nodeContent)
				if len(domainMatches) > 0 {
					url = domainMatches[0] + url
					break
				}
			}
		}
	}

	// Validate URL
	if url != "" {
		// If it's already an absolute URL, return it
		if isAbs, _ := isAbsoluteURL(url); isAbs {
			return url
		}

		// If not, try to convert it into absolute URL using default URL
		// instead of using domain name
		newURL := createAbsoluteURL(url, defaultURL)
		if isAbs, _ := isAbsoluteURL(newURL); isAbs {
			return newURL
		}
	}

	// At this point, URL is either empty or not absolute, so just return the default URL
	if defaultURL != nil {
		return defaultURL.String()
	}

	// If default URL is not specified, just give up
	return ""
}

// extractDomSitename extracts the name of a site from the main title (if it exists).
func extractDomSitename(doc *html.Node) string {
	titleNode := dom.QuerySelector(doc, "head > title")
	if titleNode == nil {
		return ""
	}

	titleText := strNormalize(dom.TextContent(titleNode))
	if titleText == "" {
		return ""
	}

	matches := rxSitenameFinder1.FindStringSubmatch(titleText)
	if len(matches) > 0 {
		return matches[1]
	}

	return ""
}

// extractDomCategories returns the categories of the document.
func extractDomCategories(doc *html.Node) []string {
	categories := []string{}

	// Try using selectors
	for _, selector := range metaCategoriesSelectors {
		for _, node := range dom.QuerySelectorAll(doc, selector) {
			href := dom.GetAttribute(node, "href")
			href = strings.TrimSpace(href)
			if href != "" && strings.Contains(href, "/category/") {
				text := dom.TextContent(node)
				text = strNormalize(text)
				if text != "" {
					categories = append(categories, text)
				}
			}
		}

		if len(categories) > 0 {
			break
		}
	}

	// Fallback
	if len(categories) == 0 {
		node := dom.QuerySelector(doc, `head meta[property="article:section"]`)
		if node != nil {
			content := dom.GetAttribute(node, "content")
			content = strNormalize(content)
			if content != "" {
				categories = append(categories, content)
			}
		}
	}

	return categories
}

// extractDomTags returns the tags of the document.
func extractDomTags(doc *html.Node) []string {
	tags := []string{}

	// Try using selectors
	for _, selector := range metaTagsSelectors {
		for _, node := range dom.QuerySelectorAll(doc, selector) {
			href := dom.GetAttribute(node, "href")
			href = strings.TrimSpace(href)
			if href != "" && strings.Contains(href, "/tags/") {
				text := dom.TextContent(node)
				text = strNormalize(text)
				if text != "" {
					tags = append(tags, text)
				}
			}
		}

		if len(tags) > 0 {
			break
		}
	}

	return tags
}

func cleanCatTags(catTags []string) []string {
	cleanedEntries := []string{}
	for _, entry := range catTags {
		for _, item := range rxCommaSeparator.Split(entry, -1) {
			if item = strNormalize(item); item != "" {
				cleanedEntries = append(cleanedEntries, item)
			}
		}
	}
	return cleanedEntries
}

func extractDomMetaSelectors(doc *html.Node, limit int, selectors ...string) string {
	for _, selector := range selectors {
		for _, node := range dom.QuerySelectorAll(doc, selector) {
			text := dom.TextContent(node)
			text = strNormalize(text)
			if text != "" && utf8.RuneCountInString(text) < limit {
				return text
			}
		}
	}

	return ""
}

func validateMetadataAuthor(author string) string {
	if author == "" {
		return author
	}

	if !strings.Contains(author, " ") || strings.HasPrefix(author, "http") {
		return ""
	}

	// Make sure author doesn't contain JSON symbols (in case JSON+LD has wrong format)
	if rxJsonSymbol.MatchString(author) {
		return ""
	}

	return author
}
