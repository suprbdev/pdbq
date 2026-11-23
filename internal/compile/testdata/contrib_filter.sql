-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."search") @@ to_tsquery($2::regconfig, $1::text) AND (__t_1."search") @@ websearch_to_tsquery($4::regconfig, $3::text)
  ORDER BY __t_1."id" ASC
  LIMIT $5
) AS __s_2
GROUP BY ()
-- args: ["cat \u0026 dog","english","\"black cat\" -dog","english",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."search") @@ phraseto_tsquery($1::text) AND (__t_1."search") @@ plainto_tsquery($2::text)
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["black cat","cats",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE __t_1."title" ILIKE $1 AND (__t_1."title") OPERATOR("ext".%) $2::text AND (__t_1."title") OPERATOR("ext".%>>) $3::text AND (__t_1."title") OPERATOR("ext".%>) $4::text
  ORDER BY __t_1."id" ASC
  LIMIT $5
) AS __s_2
GROUP BY ()
-- args: ["%sql%","postgress","gres","postgres",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."attrs") OPERATOR("ext".@>) "ext".hstore($1::text[]) AND (__t_1."attrs") OPERATOR("ext".?&) $2::text[] AND (__t_1."attrs") OPERATOR("ext".?|) $3::text[] AND (__t_1."attrs") OPERATOR("ext".?) $4::text AND __t_1."attrs" OPERATOR("ext".=) "ext".hstore($5::text[])
  ORDER BY __t_1."id" ASC
  LIMIT $6
) AS __s_2
GROUP BY ()
-- args: [["color","red","size",null],["a","b"],["c"],"color",["k","v"],101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $9), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."path") OPERATOR("ext".@>) $1::"ext".ltree AND (__t_1."path") OPERATOR("ext".<@) $2::"ext".ltree AND __t_1."path" OPERATOR("ext".>) $3::"ext".ltree AND __t_1."path" OPERATOR("ext".=) ANY($4::text[]::"ext".ltree[]) AND (__t_1."path") OPERATOR("ext".?) $5::"ext".lquery[] AND (__t_1."path") OPERATOR("ext".~) $6::"ext".lquery AND (__t_1."path") OPERATOR("ext".@) $7::"ext".ltxtquery
  ORDER BY __t_1."id" ASC
  LIMIT $8
) AS __s_2
GROUP BY ()
-- args: ["a.b.c","a","a",["a.b","a.c"],["a.*","x.*"],"*.b.*","b \u0026 !z",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."paths") OPERATOR("ext".<@) $1::"ext".ltree AND __t_1."paths" = $2::"ext".ltree[] AND (__t_1."paths") OPERATOR("ext".~) $3::"ext".lquery
  ORDER BY __t_1."id" ASC
  LIMIT $4
) AS __s_2
GROUP BY ()
-- args: ["a",["a.b","c"],"a.*",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $12), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."span") -|- int4range($1::int4, $2::int4, $3::text) AND (__t_1."span") @> $4::int4 AND __t_1."span" = int4range($5::int4, $6::int4, $7::text) AND (__t_1."span") && int4range($8::int4, $9::int4, $10::text)
  ORDER BY __t_1."id" ASC
  LIMIT $11
) AS __s_2
GROUP BY ()
-- args: ["10",null,"[)","3",null,null,"()","1","5","[)",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $9), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."during") @> tstzrange($1::timestamptz, $2::timestamptz, $3::text) AND (__t_1."during") @> $4::timestamptz AND (__t_1."during") << tstzrange($5::timestamptz, $6::timestamptz, $7::text)
  ORDER BY __t_1."id" ASC
  LIMIT $8
) AS __s_2
GROUP BY ()
-- args: ["2024-01-01T00:00:00Z","2024-02-01T00:00:00Z","[)","2024-01-15T12:00:00Z","2030-01-01T00:00:00Z",null,"[)",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $9), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."avail") @> $1::int4 AND __t_1."avail" = int4multirange(VARIADIC ARRAY(SELECT int4range(__l, __u, __b) FROM unnest($2::int4[], $3::int4[], $4::text[]) AS __r(__l, __u, __b))) AND (__t_1."avail") && int4range($5::int4, $6::int4, $7::text)
  ORDER BY __t_1."id" ASC
  LIMIT $8
) AS __s_2
GROUP BY ()
-- args: ["4",["1","5"],["2",null],["[)","[)"],"1","3","[]",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $9), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."ip") << $1::inet AND (__t_1."ip") <<= $2::inet AND (__t_1."ip") && $3::inet AND (__t_1."ip") >>= $4::inet AND __t_1."ip" = $5::inet AND __t_1."ip" <> ALL($6::text[]::inet[]) AND (__t_1."net") >> $7::inet
  ORDER BY __t_1."id" ASC
  LIMIT $8
) AS __s_2
GROUP BY ()
-- args: ["10.0.0.0/8","10.0.0.0/8","10.0.0.0/24","10.1.0.0/16","10.0.0.2",["10.0.0.1"],"10.1.2.3",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."path") OPERATOR("ext".<@) $1::"ext".ltree OR NOT ((__t_1."attrs") OPERATOR("ext".?) $2::text)) AND __t_1."search" IS NOT NULL
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["a","x",101,100]

-- field: allDocuments
-- error: compile: config must name a text search configuration

