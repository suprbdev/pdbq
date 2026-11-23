-- field: createLocation
WITH __mut AS (
  INSERT INTO "public"."locations" ("box", "pos", "spot")
  VALUES ($1::"ext".cube, $2::"ext".cube, "ext".ll_to_earth($3::float8, $4::float8))
  RETURNING "id", "pos", "box", "spot"
)
SELECT json_build_object('location', json_build_object('id', __t_1."id", 'pos', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((__t_1."pos")::text, '()', '[]') || ']')::json) AS __cube(j)), 'box', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((__t_1."box")::text, '()', '[]') || ']')::json) AS __cube(j)), 'spot', (SELECT json_build_object('lat', "ext".latitude(__ll_2.e), 'lon', "ext".longitude(__ll_2.e)) FROM (SELECT __t_1."spot" AS e) AS __ll_2 WHERE __ll_2.e IS NOT NULL))) AS data
FROM __mut AS __t_1
-- args: ["(0, 0), (1, 1)","(1, 2, 3)",51.5,-0.12]

-- field: updateLocationById
WITH __mut AS (
  UPDATE "public"."locations"
  SET "box" = $1::"ext".cube, "home" = "ext".ll_to_earth($2::float8, $3::float8), "spot" = $4
  WHERE "public"."locations"."id" = $5
  RETURNING "box", "home"
)
SELECT json_build_object('location', json_build_object('box', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((__t_1."box")::text, '()', '[]') || ']')::json) AS __cube(j)), 'home', (SELECT json_build_object('lon', "ext".longitude(__ll_2.e)) FROM (SELECT __t_1."home" AS e) AS __ll_2 WHERE __ll_2.e IS NOT NULL))) AS data
FROM __mut AS __t_1
-- args: ["(0, 0),(2, 2)",-33.9,151.2,null,1]

-- field: createLocation
-- error: compile: cube value must have at least one coordinate

