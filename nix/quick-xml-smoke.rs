#[cfg(test)]
mod tests {
    #[derive(Debug, serde::Deserialize)]
    struct List { #[serde(rename="Name")] name: String }
    macro_rules! ordinary_xml {
        ($test:ident, $xml:ident) => {
            #[test]
            fn $test() {
                let parsed: List = $xml::de::from_str("<List><Name>ordinary</Name></List>").unwrap();
                assert_eq!(parsed.name, "ordinary");
                let attrs: String = (0..48).map(|n| format!(" a{n}='v{n}'")).collect();
                let text = format!("<row{attrs}/>");
                let mut reader = $xml::Reader::from_str(&text);
                match reader.read_event().unwrap() {
                    $xml::events::Event::Empty(start) => assert_eq!(start.attributes().collect::<Result<Vec<_>,_>>().unwrap().len(),48),
                    _ => panic!("ordinary empty row expected"),
                }
                #[cfg(feature="patched_configuration")]
                {
                let mut namespaces = $xml::NsReader::from_str("<a xmlns:p='urn:one' xmlns:q='urn:two'/>");
                namespaces.resolver_mut().set_max_declarations_per_element(1);
                assert!(namespaces.read_event().unwrap_err().to_string().contains("namespace bindings"));
                let mut namespaces = $xml::NsReader::from_str("<a xmlns:p='urn:one'/>");
                namespaces.resolver_mut().set_max_declarations_per_element(1);
                assert!(namespaces.read_event().is_ok());
                }
            }
        }
    }
    macro_rules! selected_deserializer_boundary {
        ($test:ident, $xml:ident) => {
            #[test]
            fn $test() {
                let declarations: String = (0..257).map(|n| format!(" xmlns:p{n}='urn:scope{n}'")).collect();
                let document = format!("<List{declarations}><Name>ordinary</Name></List>");
                let result = $xml::de::from_str::<List>(&document);
                assert!(result.is_err(), "selected cloud-response deserializer accepted namespaces beyond default bound");
                assert!(result.unwrap_err().to_string().contains("namespace bindings"));
                let declarations: String = (0..256).map(|n| format!(" xmlns:p{n}='urn:scope{n}'")).collect();
                let document = format!("<List{declarations}><Name>ordinary</Name></List>");
                assert_eq!($xml::de::from_str::<List>(&document).unwrap().name, "ordinary");
                let attrs: String = (0..64).map(|n| format!(" a{n}='v{n}'")).collect();
                // Cover names recorded both before and after the hash threshold.
                for duplicate_index in [16, 63] {
                let duplicate = format!("<row{attrs} a{duplicate_index}='duplicate'/>");
                let mut reader = $xml::Reader::from_str(&duplicate);
                match reader.read_event().unwrap() {
                    $xml::events::Event::Empty(start) => assert!(start.attributes().collect::<Result<Vec<_>,_>>().is_err()),
                    _ => panic!("ordinary empty row expected"),
                }
                }
            }
        }
    }
    selected_deserializer_boundary!(selected_xml_037, xml037);
    selected_deserializer_boundary!(selected_xml_038, xml038);
    ordinary_xml!(ordinary_xml_037, xml037);
    ordinary_xml!(ordinary_xml_038, xml038);
}
