-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'labelSimilarity', "ext".similarity(__t_1."label", $1::text), 'labelWordSimilarity', "ext".word_similarity($2::text, __t_1."label"), 'labelStrictWordSimilarity', "ext".strict_word_similarity($3::text, __t_1."label"), 'titleSimilarity', "ext".similarity(__t_1."title", $4::text)) AS ndata, row_number() OVER (ORDER BY (__t_1."label") OPERATOR("ext".<->) $5::text ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY (__t_1."label") OPERATOR("ext".<->) $5::text ASC, __t_1."id" ASC
  LIMIT 6
) AS __s_2
GROUP BY ()
-- args: ["postgres","postgres","postgres","pg","postgres",5]

-- field: allDocuments
-- error: compile: field titleSimilarity requires near: {title: …}

-- field: allDocuments
-- error: compile: field labelSimilarity requires near: {label: …}

